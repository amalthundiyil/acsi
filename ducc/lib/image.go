package lib

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/docker/docker/image"
	"github.com/olekukonko/tablewriter"
	log "github.com/sirupsen/logrus"

	constants "github.com/cvmfs/ducc/constants"
	cvmfs "github.com/cvmfs/ducc/cvmfs"
	da "github.com/cvmfs/ducc/docker-api"
	l "github.com/cvmfs/ducc/log"
	temp "github.com/cvmfs/ducc/temp"
)

type ManifestRequest struct {
	Image    Image
	Password string
}

type Image struct {
	Id          int
	User        string
	Scheme      string
	Registry    string
	Repository  string
	Tag         string
	Digest      string
	IsThin      bool
	TagWildcard bool
	Manifest    *da.Manifest
}

func (i *Image) GetSimpleName() string {
	name := fmt.Sprintf("%s/%s", i.Registry, i.Repository)
	if i.Tag == "" {
		return name
	} else {
		return name + ":" + i.Tag
	}
}

func (i *Image) WholeName() string {
	root := fmt.Sprintf("%s://%s/%s", i.Scheme, i.Registry, i.Repository)
	if i.Tag != "" {
		root = fmt.Sprintf("%s:%s", root, i.Tag)
	}
	if i.Digest != "" {
		root = fmt.Sprintf("%s@%s", root, i.Digest)
	}
	return root
}

func (i *Image) GetManifestUrl() string {
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/", i.Scheme, i.Registry, i.Repository)
	if i.Digest != "" {
		url = fmt.Sprintf("%s%s", url, i.Digest)
	} else {
		url = fmt.Sprintf("%s%s", url, i.Tag)
	}
	return url
}

func (i *Image) GetReference() string {
	if i.Digest == "" && i.Tag != "" {
		return ":" + i.Tag
	}
	if i.Digest != "" && i.Tag == "" {
		return "@" + i.Digest
	}
	if i.Digest != "" && i.Tag != "" {
		return ":" + i.Tag + "@" + i.Digest
	}
	panic("Image wrong format, missing both tag and digest")
}

func (i *Image) GetSimpleReference() string {
	if i.Tag != "" {
		return i.Tag
	}
	if i.Digest != "" {
		return i.Digest
	}
	panic("Image wrong format, missing both tag and digest")
}

func (img *Image) PrintImage(machineFriendly, csv_header bool) {
	if machineFriendly {
		if csv_header {
			fmt.Printf("name,user,scheme,registry,repository,tag,digest,is_thin\n")
		}
		fmt.Printf("%s,%s,%s,%s,%s,%s,%s,%s\n",
			img.WholeName(), img.User, img.Scheme,
			img.Registry, img.Repository,
			img.Tag, img.Digest,
			fmt.Sprint(img.IsThin))
	} else {
		table := tablewriter.NewWriter(os.Stdout)
		table.SetAlignment(tablewriter.ALIGN_LEFT)
		table.SetHeader([]string{"Key", "Value"})
		table.Append([]string{"Name", img.WholeName()})
		table.Append([]string{"User", img.User})
		table.Append([]string{"Scheme", img.Scheme})
		table.Append([]string{"Registry", img.Registry})
		table.Append([]string{"Repository", img.Repository})
		table.Append([]string{"Tag", img.Tag})
		table.Append([]string{"Digest", img.Digest})
		var is_thin string
		if img.IsThin {
			is_thin = "true"
		} else {
			is_thin = "false"
		}
		table.Append([]string{"IsThin", is_thin})
		table.Render()
	}
}

func (img *Image) GetManifest() (da.Manifest, error) {
	if img.Manifest != nil {
		return *img.Manifest, nil
	}
	bytes, err := img.getByteManifest()
	if err != nil {
		return da.Manifest{}, err
	}
	var manifest da.Manifest
	err = json.Unmarshal(bytes, &manifest)
	if err != nil {
		return manifest, err
	}
	if reflect.DeepEqual(da.Manifest{}, manifest) {
		return manifest, fmt.Errorf("Got empty manifest")
	}
	img.Manifest = &manifest
	return manifest, nil
}

func (img *Image) GetChanges() (changes []string, err error) {
	user := img.User
	pass, err := GetPassword()
	if err != nil {
		l.LogE(err).Warning("Unable to get the credential for downloading the configuration blog, trying anonymously")
		user = ""
		pass = ""
	}

	changes = []string{"ENV CVMFS_IMAGE true"}
	manifest, err := img.GetManifest()
	if err != nil {
		l.LogE(err).Warning("Impossible to retrieve the manifest of the image, not changes set")
		return
	}
	configUrl := fmt.Sprintf("%s://%s/v2/%s/blobs/%s",
		img.Scheme, img.Registry, img.Repository, manifest.Config.Digest)
	token, err := firstRequestForAuth(configUrl, user, pass)
	if err != nil {
		l.LogE(err).Warning("Impossible to retrieve the token for getting the changes from the repository, not changes set")
		return
	}
	client := &http.Client{}
	req, err := http.NewRequest("GET", configUrl, nil)
	if err != nil {
		l.LogE(err).Warning("Impossible to create a request for getting the changes no chnages set.")
		return
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	resp, err := client.Do(req)
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		l.LogE(err).Warning("Error in reading the body from the configuration, no change set")
		return
	}

	var config image.Image
	err = json.Unmarshal(body, &config)
	if err != nil {
		l.LogE(err).Warning("Error in unmarshaling the configuration of the image")
		return
	}
	env := config.Config.Env

	if len(env) > 0 {
		for _, e := range env {
			envs := strings.SplitN(e, "=", 2)
			if len(envs) != 2 {
				continue
			}
			change := fmt.Sprintf("ENV %s=\"%s\"", envs[0], envs[1])
			changes = append(changes, change)
		}
	}

	cmd := config.Config.Cmd

	if len(cmd) > 0 {
		command := fmt.Sprintf("CMD")
		for _, c := range cmd {
			command = fmt.Sprintf("%s %s", command, c)
		}
		changes = append(changes, command)
	}

	return
}

func (img *Image) GetSingularityLocation() string {
	return fmt.Sprintf("docker://%s/%s%s", img.Registry, img.Repository, img.GetReference())
}

func (img *Image) GetTagListUrl() string {
	return fmt.Sprintf("%s://%s/v2/%s/tags/list", img.Scheme, img.Registry, img.Repository)
}

func (img *Image) ExpandWildcard() (<-chan *Image, <-chan *Image, error) {
	r1 := make(chan *Image, 500)
	r2 := make(chan *Image, 500)
	var wg sync.WaitGroup
	defer func() {
		go func() {
			wg.Wait()
			close(r1)
			close(r2)
		}()
	}()
	if !img.TagWildcard {
		img.GetManifest()
		r1 <- img
		r2 <- img
		return r1, r2, nil
	}
	var tagsList struct {
		Tags []string
	}
	pass, err := GetPassword()
	if err != nil {
		l.LogE(err).Warning("Unable to retrieve the password, trying to get the manifest anonymously.")
		pass = ""
	}
	user := img.User
	url := img.GetTagListUrl()
	token, err := firstRequestForAuth(url, user, pass)
	if err != nil {
		errF := fmt.Errorf("Error in authenticating for retrieving the tags: %s", err)
		l.LogE(err).Error(errF)
		return r1, r2, errF
	}

	client := http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", token)

	resp, err := client.Do(req)
	if err != nil {
		errF := fmt.Errorf("Error in making the request for retrieving the tags: %s", err)
		l.LogE(err).WithFields(log.Fields{"url": url}).Error(errF)
		return r1, r2, errF
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		errF := fmt.Errorf("Got error status code (%d) trying to retrieve the tags", resp.StatusCode)
		l.LogE(err).WithFields(log.Fields{"status code": resp.StatusCode, "url": url}).Error(errF)
		return r1, r2, errF
	}
	if err = json.NewDecoder(resp.Body).Decode(&tagsList); err != nil {
		errF := fmt.Errorf("Error in decoding the tags from the server: %s", err)
		l.LogE(err).Error(errF)
		return r1, r2, errF
	}
	pattern := img.Tag
	filteredTags, err := filterUsingGlob(pattern, tagsList.Tags)
	if err != nil {
		return r1, r2, nil
	}
	for _, tag := range filteredTags {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			taggedImg := *img
			taggedImg.Tag = tag
			taggedImg.GetManifest()
			r1 <- &taggedImg
			r2 <- &taggedImg
		}(tag)
	}

	return r1, r2, nil
}

func filterUsingGlob(pattern string, toFilter []string) ([]string, error) {
	result := make([]string, 0)
	regexPattern := strings.ReplaceAll(pattern, "*", ".*")
	regex, err := regexp.Compile(regexPattern)
	if err != nil {
		return result, err
	}
	regex.Longest()
	for _, toCheck := range toFilter {
		s := regex.FindString(toCheck)
		if s == "" {
			continue
		}
		if s == toCheck {
			result = append(result, s)
		}
	}
	return result, nil
}

// here is where in the FS we are going to store the singularity image
func (img *Image) GetSingularityPath() (string, error) {
	manifest, err := img.GetManifest()
	if err != nil {
		l.LogE(err).Error("Error in getting the manifest to figureout the singularity path")
		return "", err
	}
	return manifest.GetSingularityPath(), nil
}

// the one that the user see, without the /cvmfs/$repo.cern.ch prefix
// used mostly by Singularity
func (i *Image) GetPublicSymlinkPath() string {
	return filepath.Join(i.Registry, i.Repository+":"+i.GetSimpleReference())
}

func (img *Image) getByteManifest() ([]byte, error) {
	pass, err := GetPassword()
	if err != nil {
		l.LogE(err).Warning("Unable to retrieve the password, trying to get the manifest anonymously.")
		return img.getAnonymousManifest()
	}
	return img.getManifestWithPassword(pass)
}

func (img *Image) getAnonymousManifest() ([]byte, error) {
	return getManifestWithUsernameAndPassword(img, "", "")
}

func (img *Image) getManifestWithPassword(password string) ([]byte, error) {
	return getManifestWithUsernameAndPassword(img, img.User, password)
}

func getManifestWithUsernameAndPassword(img *Image, user, pass string) ([]byte, error) {

	url := img.GetManifestUrl()

	token, err := firstRequestForAuth(url, user, pass)
	if err != nil {
		l.LogE(err).Error("Error in getting the authentication token")
		return nil, err
	}

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		l.LogE(err).Error("Impossible to create a HTTP request")
		return nil, err
	}

	req.Header.Set("Authorization", token)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	resp, err := client.Do(req)
	if err != nil {
		l.LogE(err).Error("Error in making the HTTP request")
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		l.LogE(err).Error("Error in reading the second http response")
		return nil, err
	}
	return body, nil
}

type Credentials struct {
	username string
	password string
}

func getCredentialsFromEnv(user, pass string) (Credentials, error) {
	u := os.Getenv(user)
	p := os.Getenv(pass)
	c := Credentials{u, p}
	err := error(nil)
	if user == "" || pass == "" {
		err = fmt.Errorf("missing either username ($%s) or password ($%s) or both for accessing the docker registry", user, pass)
	}
	return c, err

}

func getDockerHubCredentials() (Credentials, error) {
	return getCredentialsFromEnv("DUCC_DOCKERHUB_USER", "DUCC_DOCKERHUB_PASS")
}

func getGitlabContainersCredentials() (Credentials, error) {
	return getCredentialsFromEnv("DUCC_GITLAB_REGISTRY_USER", "DUCC_GITLAB_REGISTRY_PASS")
}

func GetAuthToken(url string, credentials []Credentials) (token string, err error) {
	docker, err := getDockerHubCredentials()
	if err == nil {
		credentials = append(credentials, docker)
	}
	gitlab, err := getGitlabContainersCredentials()
	if err == nil {
		credentials = append(credentials, gitlab)
	}
	for _, c := range credentials {
		token, err = firstRequestForAuth_internal(url, c.username, c.password)
		if err == nil {
			return token, err
		}
	}
	return token, err
}

func firstRequestForAuth(url, user, pass string) (token string, err error) {
	c := Credentials{user, pass}
	credentials := []Credentials{c}
	return GetAuthToken(url, credentials)
}

func firstRequestForAuth_internal(url, user, pass string) (token string, err error) {
	resp, err := http.Get(url)
	if err != nil {
		l.LogE(err).Error("Error in making the first request for auth")
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 && resp.StatusCode >= 200 {
		log.WithFields(log.Fields{
			"status code": resp.StatusCode,
		}).Info("Return valid response, token not necessary.")
		return
	}
	if resp.StatusCode != 401 {
		log.WithFields(log.Fields{
			"status code": resp.StatusCode,
		}).Info("Expected status code 401, print body anyway.")
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			l.LogE(err).Error("Error in reading the first http response")
		}
		fmt.Println(string(body))
		return "", err
	}
	WwwAuthenticate := resp.Header["Www-Authenticate"][0]
	// we first try to get the token with the authentication
	// if we fail, and we might since the docker hub might not have our user
	// we try again without authentication
	token, err = requestAuthToken(WwwAuthenticate, user, pass)
	if err == nil {
		// happy path
		return token, nil
	}
	// some error, we should retry without auth
	if user != "" || pass != "" {
		token, err = requestAuthToken(WwwAuthenticate, "", "")
		if err == nil {
			// happy path without auth
			return token, nil
		}
	}
	l.LogE(err).Error("Error in getting the authentication token")
	return "", err
}

func getLayerUrl(img *Image, layerDigest string) string {
	return fmt.Sprintf("%s://%s/v2/%s/blobs/%s",
		img.Scheme, img.Registry, img.Repository, layerDigest)
}

type downloadedLayer struct {
	Name string
	Path *ReadAndHash
}

func (d *downloadedLayer) IngestIntoCVMFS(CVMFSRepo string) error {
	layerDigest := strings.Split(d.Name, ":")[1]
	layerPath := cvmfs.LayerRootfsPath(CVMFSRepo, layerDigest)
	if _, err := os.Stat(layerPath); err == nil {
		// the layer already exists
		return nil
	}
	superDir := filepath.Dir(filepath.Dir(cvmfs.TrimCVMFSRepoPrefix(layerPath)))
	go cvmfs.CreateCatalogIntoDir(CVMFSRepo, superDir)
	ingestPath := cvmfs.TrimCVMFSRepoPrefix(layerPath)
	err := cvmfs.Ingest(CVMFSRepo, d.Path,
		"--catalog", "-t", "-",
		"-b", ingestPath)
	if err != nil {
		l.LogE(err).WithFields(
			log.Fields{"layer": d.Name}).
			Error("Some error in ingest the layer")
		go cvmfs.IngestDelete(CVMFSRepo, ingestPath)
		return err
	}
	err = StoreLayerInfo(CVMFSRepo, layerDigest, d.Path)
	if err != nil {
		return err
	}
	return nil
}

func (img *Image) GetLayers(layersChan chan<- downloadedLayer, manifestChan chan<- string, stopGettingLayers <-chan bool, rootPath string) error {
	defer close(layersChan)
	defer close(manifestChan)

	layerDownloader := NewLayerDownloader(img)
	_, err := layerDownloader.getToken()
	if err != nil {
		return err
	}

	// then we try to get the manifest from our database
	manifest, err := img.GetManifest()
	if err != nil {
		l.LogE(err).Warn("Error in getting the manifest")
		return err
	}

	killKiller := make(chan bool, 1)
	errorChannel := make(chan error, 1)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {

		select {

		case <-killKiller:
			return
		case <-stopGettingLayers:
			err := fmt.Errorf("Detect errors, stop getting layer")
			errorChannel <- err
			l.LogE(err).Error("Detect error, stop getting layers")
			cancel()
			return
		}
	}()
	defer func() { killKiller <- true }()

	var wg sync.WaitGroup
	defer wg.Wait()
	// at this point we iterate each layer and we download it.
	for _, layer := range manifest.Layers {
		wg.Add(1)
		go func(ctx context.Context, layer da.Layer) {
			defer wg.Done()
			l.Log().WithFields(
				log.Fields{"layer": layer.Digest}).
				Info("Start working on layer")
			toSend, err := layerDownloader.DownloadLayer(layer)
			if err != nil {
				l.LogE(err).Error("Error in downloading a layer")
				return
			}
			select {
			case layersChan <- toSend:
				return
			case <-ctx.Done():
				return
			}
		}(ctx, layer)
	}

	// finally we marshal the manifest and store it into a file
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		l.LogE(err).Error("Error in marshaling the manifest")
		return err
	}
	manifestPath := filepath.Join(rootPath, "manifest.json")
	err = ioutil.WriteFile(manifestPath, manifestBytes, 0666)
	if err != nil {
		l.LogE(err).Error("Error in writing the manifest to file")
		return err
	}
	// ship the manifest file
	manifestChan <- manifestPath

	// we wait here to make sure that the channel is populated
	wg.Wait()
	select {
	case err := <-errorChannel:
		return err
	default:
		return nil
	}
}

func (img *Image) downloadLayer(layer da.Layer, token string) (toSend downloadedLayer, err error) {
	user := img.User
	pass, err := GetPassword()
	if err != nil {
		l.LogE(err).Warning("Unable to retrieve the password, trying to get the layers anonymously.")
		user = ""
		pass = ""
	}
	layerUrl := getLayerUrl(img, layer.Digest)
	if token == "" {
		token, err = firstRequestForAuth(layerUrl, user, pass)
		if err != nil {
			return
		}
	}
	for i := 0; i <= 5; i++ {
		err = nil
		client := &http.Client{}
		req, errR := http.NewRequest("GET", layerUrl, nil)
		if err != nil {
			l.LogE(errR).Error("Impossible to create the HTTP request.")
			err = errR
			break
		}
		req.Header.Set("Authorization", token)
		resp, errReq := client.Do(req)
		l.Log().WithFields(
			log.Fields{"layer": layer.Digest}).
			Info("Make request for layer")
		if errReq != nil {
			err = errReq
			break
		}
		if 200 <= resp.StatusCode && resp.StatusCode < 300 {
			gread, errG := gzip.NewReader(resp.Body)
			if errG != nil {
				err = errG
				l.LogE(err).Warning("Error in creating the zip to unzip the layer")
				continue
			}
			path := NewReadAndHash(gread)
			toSend = downloadedLayer{Name: layer.Digest, Path: path}
			return toSend, nil
		} else {
			err = fmt.Errorf("Layer not received, status code: %d", resp.StatusCode)
			l.LogE(err).Warning("Received status code ", resp.StatusCode)
			if resp.StatusCode == 401 {
				// try to get the token again
				newToken, errToken := firstRequestForAuth(layerUrl, user, pass)
				if errToken != nil {
					l.LogE(errToken).Warning("Error in refreshing the token")
				} else {
					token = newToken
				}
			}
		}
	}
	l.LogE(err).Warning("return from error path")
	return
}

func parseBearerToken(token string) (realm string, options map[string]string, err error) {
	options = make(map[string]string)
	args := token[7:]
	keyValue := strings.Split(args, ",")
	for _, kv := range keyValue {
		splitted := strings.Split(kv, "=")
		if len(splitted) != 2 {
			err = fmt.Errorf("Wrong formatting of the token")
			return
		}
		splitted[1] = strings.Trim(splitted[1], `"`)
		if splitted[0] == "realm" {
			realm = splitted[1]
		} else {
			options[splitted[0]] = splitted[1]
		}
	}
	return
}

func requestAuthToken(token, user, pass string) (authToken string, err error) {
	realm, options, err := parseBearerToken(token)
	if err != nil {
		return
	}
	req, err := http.NewRequest("GET", realm, nil)
	if err != nil {
		return
	}

	query := req.URL.Query()
	for k, v := range options {
		query.Add(k, v)
	}
	if user != "" && pass != "" {
		query.Add("offline_token", "true")
		req.SetBasicAuth(user, pass)
	}
	req.URL.RawQuery = query.Encode()

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("Error in getting the token, http request failed %s", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		err = fmt.Errorf("Authorization error %s", resp.Status)
		return
	}

	var jsonResp map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&jsonResp)
	if err != nil {
		return
	}
	authTokenInterface, ok := jsonResp["token"]
	if ok {
		authToken = "Bearer " + authTokenInterface.(string)
	} else {
		err = fmt.Errorf("Didn't get the token key from the server")
		return
	}
	return
}

type LayerDownloader struct {
	image *Image
	token string
	lock  sync.Mutex
}

func NewLayerDownloader(image *Image) LayerDownloader {
	return LayerDownloader{image: image, token: ""}
}

func (ld *LayerDownloader) getToken() (token string, err error) {
	ld.lock.Lock()
	defer ld.lock.Unlock()
	if ld.token != "" {
		return ld.token, nil
	}
	manifest, err := ld.image.GetManifest()
	if err != nil {
		return
	}
	user := ld.image.User
	pass, err := GetPassword()
	if err != nil {
		l.LogE(err).Warning("Unable to retrieve the password, trying to get the layers anonymously.")
		user = ""
		pass = ""
	}

	firstLayer := manifest.Layers[0]
	layerUrl := getLayerUrl(ld.image, firstLayer.Digest)
	token, err = firstRequestForAuth(layerUrl, user, pass)
	if err != nil {
		return
	}
	ld.token = token
	return
}

func (ld *LayerDownloader) DownloadLayer(layer da.Layer) (downloadedLayer, error) {
	token, err := ld.getToken()
	if err != nil {
		return downloadedLayer{}, err
	}
	return ld.image.downloadLayer(layer, token)
}

func (ld *LayerDownloader) DownloadAndIngest(CVMFSRepo string, layer da.Layer) error {
	err := error(nil)
	for i := 0; i <= 5; i += 1 {
		to_ingest, err := ld.DownloadLayer(layer)
		if err != nil {
			// let's try again
			continue
		}
		err = to_ingest.IngestIntoCVMFS(CVMFSRepo)
		if err == nil {
			return nil
		}
	}
	return err
}

func (img *Image) CreateChainStructure(CVMFSRepo string) (err error, lastChainId string) {
	// make sure we have the layers somewhere
	manifest, err := img.GetManifest()
	if err != nil {
		return
	}
	layerToDownload := []da.Layer{}
	for _, layer := range manifest.Layers {
		// we download the layer if they are not already in storage
		layerDigest := strings.Split(layer.Digest, ":")[1]
		path := cvmfs.LayerPath(CVMFSRepo, layerDigest)
		if _, err := os.Stat(path); err != nil {
			layerToDownload = append(layerToDownload, layer)
		}
	}
	if len(layerToDownload) > 0 {
		ld := NewLayerDownloader(img)
		var wg sync.WaitGroup
		for _, layer := range layerToDownload {
			wg.Add(1)
			go func(layer da.Layer) {
				defer wg.Done()
				err := ld.DownloadAndIngest(CVMFSRepo, layer)
				if err != nil {
					l.LogE(err).
						Error("Error in ingesting the layer")
				}
			}(layer)
		}
		wg.Wait()
		for _, layer := range manifest.Layers {
			layerDigest := strings.Split(layer.Digest, ":")[1]
			path := cvmfs.LayerPath(CVMFSRepo, layerDigest)
			if _, err = os.Stat(path); err != nil {
				err = fmt.Errorf("%s: Error, impossible to get all layers in the CVMFS storage", err)
				l.LogE(err).Error("Error in getting layers in CVMFS repo")
				return
			}
		}
	}
	// then we start creating the chain structure
	chainIDs := manifest.GetChainIDs()

	paths := []string{}
	for _, chain := range chainIDs {
		path := cvmfs.ChainPath(CVMFSRepo, chain.String())
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); err != nil {
			paths = append(paths, dir)
		}
	}

	if len(paths) > 0 {
		err = cvmfs.WithinTransaction(CVMFSRepo, func() error {
			for _, dir := range paths {
				if err := os.MkdirAll(dir, constants.DirPermision); err != nil {
					return nil
				}
			}
			return nil
		})
		if err != nil {
			l.LogE(err).Error("Impossible to create directory to contains the chainID")
			return
		}
	}

	for i, chain := range chainIDs {
		digest := chain.String()
		lastChainId = digest

		path := cvmfs.ChainPath(CVMFSRepo, digest)

		if _, err := os.Stat(path); err == nil {
			// the chain is present, we skip the loop
			continue
		}
		previous := ""
		if i != 0 {
			previous = chainIDs[i-1].String()
		}
		err = cvmfs.CreateChain(CVMFSRepo,
			chain.String(),
			previous,
			manifest.Layers[i].Digest)
		if err != nil {
			l.LogE(err).Error("Error in creating the chain")
			return
		}
	}
	return
}

func (img *Image) CreateSneakyChainStructure(CVMFSRepo string) (err error, lastChainId string) {
	// make sure we have the layers somewhere
	manifest, err := img.GetManifest()
	if err != nil {
		return
	}

	// then we start creating the chain structure
	chainIDs := manifest.GetChainIDs()

	paths := []string{}
	for _, chain := range chainIDs {
		path := cvmfs.ChainPath(CVMFSRepo, chain.String())
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); err != nil {
			paths = append(paths, dir)
		}
	}

	if len(paths) > 0 {
		err = cvmfs.WithinTransaction(CVMFSRepo, func() error {
			for _, dir := range paths {
				if err := os.MkdirAll(dir, constants.DirPermision); err != nil {
					return nil
				}
			}
			return nil
		})
		if err != nil {
			l.LogE(err).Error("Impossible to create directory to contains the chainID")
			return
		}
	}

	ld := NewLayerDownloader(img)
	for i, chain := range chainIDs {
		digest := chain.String()
		lastChainId = digest

		path := cvmfs.ChainPath(CVMFSRepo, digest)

		if _, err := os.Stat(path); err == nil {
			// the chain is present, we skip the loop
			continue
		}
		previous := ""
		if i != 0 {
			previous = chainIDs[i-1].String()
		}
		downloadLayer := func(attempt int) error {
			// we need to get the layer tar reader here
			layerStream, err := ld.DownloadLayer(manifest.Layers[i])
			if err != nil {
				l.LogE(err).Error("Error in downloading the layer from the docker registry")
				return err
			}
			// TODO(smosciat) this idea of saving in a file and then re-try can be a good idea
			// maybe it can be implemented on lower level of
			// the first time we just try to read from a network stream
			// if this fail, we try to write to a real file,
			// and then read from that file
			var tarReader tar.Reader
			if attempt == 0 {
				tarReader = *tar.NewReader(layerStream.Path)
			} else {
				f, err := temp.UserDefinedTempFile()
				if err != nil {
					l.LogE(err).Error("Error in creating a temporary file")
					return err
				}
				defer os.Remove(f.Name())
				defer f.Close()
				l.Log().Info("Coping layer into file: ", f.Name())
				n, err := io.Copy(f, layerStream.Path)
				if err != nil {
					l.LogE(err).Error("Error in writing the stream into a standard file")
					return err
				}
				if _, err = f.Seek(0, 0); err != nil {
					l.LogE(err).Error("Error in seeking the file")
					return err
				}
				l.Log().Info("Written into disk N bytes: ", n)
				tarReader = *tar.NewReader(f)
			}
			err = cvmfs.CreateSneakyChain(CVMFSRepo,
				chain.String(),
				previous,
				tarReader)
			return err
		}
		for attempt := 0; attempt < 5; attempt++ {
			l.Log().Info("Start attempt: ", attempt)
			err = downloadLayer(attempt)
			if err == nil {
				l.Log().Info("Attempt ", attempt, " success")
				break
			}
			l.Log().Warn("Attempt ", attempt, " fail")
		}
		if err != nil {
			l.LogE(err).Error("Error in creating the chain")
			return err, lastChainId
		}
	}
	return
}
