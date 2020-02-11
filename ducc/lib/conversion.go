package lib

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	da "github.com/cvmfs/ducc/docker-api"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	log "github.com/sirupsen/logrus"
)

var NoPasswordError = 101

type ConversionResult int

const (
	ConversionNotFound = iota
	ConversionMatch    = iota
	ConversionNotMatch = iota
)

var subDirInsideRepo = ".layers"

func assureValidSingularity() error {
	err, stdout, _ := ExecCommand("singularity", "--version").StartWithOutput()
	if err != nil {
		err := fmt.Errorf("No working version of Singularity: %s", err)
		LogE(err).Error("No working version of Singularity")
		return err
	}
	version := stdout.String()
	sems := strings.Split(version, ".")
	if len(sems) < 2 {
		err := fmt.Errorf("Singularity version returned an unexpected format, unable to find Major and Minor number")
		LogE(err).WithFields(log.Fields{"version": version}).Error("Not valid singularity")
		return err
	}
	majorS := sems[0]
	majorI, err := strconv.Atoi(majorS)
	if err != nil {
		errF := fmt.Errorf("Singularity version returned an unexpected format, unable to parse Major number: %s", err)
		LogE(errF).WithFields(log.Fields{"version": version, "major number": majorS}).Error("Not valid singularity")
		return errF
	}
	if majorI >= 4 {
		return nil
	}
	minorS := sems[1]
	minorI, err := strconv.Atoi(minorS)
	if err != nil {
		errF := fmt.Errorf("Singularity version returned an unexpected format, unable to parse Minor number: %s", err)
		LogE(errF).WithFields(log.Fields{"version": version, "minor number": minorS}).Error("Not valid singularity")
		return errF
	}
	if majorI >= 3 && minorI >= 5 {
		return nil
	}
	errF := fmt.Errorf("Installed singularity is too old, we need at least 3.5: Installed version: %s", version)
	LogE(errF).WithFields(log.Fields{"version": version}).Error("Too old singularity")
	return errF
}

func ConvertWishSingularity(wish WishFriendly) (err error) {
	err = assureValidSingularity()
	if err != nil {
		return err
	}
	tmpDir, err := UserDefinedTempDir("", "conversion")
	if err != nil {
		LogE(err).Error("Error when creating tmp singularity directory")
		return
	}
	defer os.RemoveAll(tmpDir)
	inputImage, err := ParseImage(wish.InputName)
	expandedImgTag, err := inputImage.ExpandWildcard()
	if err != nil {
		LogE(err).WithFields(log.Fields{
			"input image": fmt.Sprintf("%s/%s", inputImage.Registry, inputImage.Repository)}).
			Error("Error in retrieving all the tags from the image")
		return
	}
	var firstError error
	for _, inputImage := range expandedImgTag {
		singularity, err := inputImage.DownloadSingularityDirectory(tmpDir)
		if err != nil {
			LogE(err).Error("Error in dowloading the singularity image")
			firstError = err
		}

		err = singularity.IngestIntoCVMFS(wish.CvmfsRepo)
		if err != nil {
			LogE(err).Error("Error in ingesting the singularity image into the CVMFS repository")
			firstError = err
		}

		os.RemoveAll(singularity.TempDirectory)
	}

	return firstError
}

func ConvertWishDocker(wish WishFriendly, convertAgain, forceDownload bool) (err error) {

	err = CreateCatalogIntoDir(wish.CvmfsRepo, subDirInsideRepo)
	if err != nil {
		LogE(err).WithFields(log.Fields{
			"directory": subDirInsideRepo}).Error(
			"Impossible to create subcatalog in super-directory.")
	}
	err = CreateCatalogIntoDir(wish.CvmfsRepo, ".flat")
	if err != nil {
		LogE(err).WithFields(log.Fields{
			"directory": ".flat"}).Error(
			"Impossible to create subcatalog in super-directory.")
	}

	outputImage, err := ParseImage(wish.OutputName)
	outputImage.User = wish.UserOutput
	if err != nil {
		return
	}
	inputImage, err := ParseImage(wish.InputName)
	inputImage.User = wish.UserInput
	if err != nil {
		return
	}
	var firstError error
	expandedImgTags, err := inputImage.ExpandWildcard()
	if err != nil {
		LogE(err).WithFields(log.Fields{
			"input image": fmt.Sprintf("%s/%s", inputImage.Registry, inputImage.Repository)}).
			Error("Error in retrieving all the tags from the image")
		return err
	}
	for _, expandedImgTag := range expandedImgTags {
		err = convertInputOutput(expandedImgTag, outputImage, wish.CvmfsRepo, convertAgain, forceDownload)
		if err != nil && firstError == nil {
			firstError = err
		}
	}
	return firstError
}

func convertInputOutput(inputImage, outputImage Image, repo string, convertAgain, forceDownload bool) (err error) {
	password, err := GetPassword()
	if err != nil {
		return
	}
	manifest, err := inputImage.GetManifest()
	if err != nil {
		return
	}

	alreadyConverted := AlreadyConverted(repo, inputImage, manifest.Config.Digest)
	Log().WithFields(log.Fields{"alreadyConverted": alreadyConverted}).Info(
		"Already converted the image, skipping.")

	switch alreadyConverted {

	case ConversionMatch:
		{
			Log().Info("Already converted the image.")
			if convertAgain == false {
				return nil
			}

		}
	}
	layersChanell := make(chan downloadedLayer, 3)
	manifestChanell := make(chan string, 1)
	stopGettingLayers := make(chan bool, 1)
	noErrorInConversion := make(chan bool, 1)

	type LayerRepoLocation struct {
		Digest   string
		Location string //location does NOT need the prefix `/cvmfs`
	}
	layerRepoLocationChan := make(chan LayerRepoLocation, 3)
	layerDigestChan := make(chan string, 3)
	go func() {
		noErrors := true
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(layerRepoLocationChan)
			close(layerDigestChan)
		}()
		defer func() {
			noErrorInConversion <- noErrors
			stopGettingLayers <- true
			close(stopGettingLayers)
		}()
		cleanup := func(location string) {
			Log().Info("Running clean up function deleting the last layer.")

			err := ExecCommand("cvmfs_server", "abort", "-f", repo).Start()
			if err != nil {
				LogE(err).Warning("Error in the abort command inside the cleanup function, this warning is usually normal")
			}

			err = ExecCommand("cvmfs_server", "ingest", "--delete", location, repo).Start()
			if err != nil {
				LogE(err).Error("Error in the cleanup command")
			}
		}
		for layer := range layersChanell {

			Log().WithFields(log.Fields{"layer": layer.Name}).Info("Start Ingesting the file into CVMFS")
			layerDigest := strings.Split(layer.Name, ":")[1]
			layerPath := LayerRootfsPath(repo, layerDigest)

			var pathExists bool
			if _, err := os.Stat(layerPath); os.IsNotExist(err) {
				pathExists = false
			} else {
				pathExists = true
			}

			// need to run this into a goroutine to avoid a deadlock
			wg.Add(1)
			go func(layerName, layerLocation, layerDigest string) {
				layerRepoLocationChan <- LayerRepoLocation{
					Digest:   layerName,
					Location: layerLocation}
				layerDigestChan <- layerDigest
				wg.Done()
			}(layer.Name, layerPath, layerDigest)

			if pathExists == false || forceDownload {

				// need to create the "super-directory", those
				// directory starting with 2 char prefix of the
				// digest itself, and put a .cvmfscatalog files
				// in it, if the directory still doesn't
				// exists. Similarly we need a .cvmfscatalog in
				// the layerfs directory, the one that host the
				// whole layer

				for _, dir := range []string{
					filepath.Dir(filepath.Dir(TrimCVMFSRepoPrefix(layerPath))),
					//TrimCVMFSRepoPrefix(layerPath)} {
				} {

					Log().WithFields(log.Fields{"catalogdirectory": dir}).Info("Working on CATALOGDIRECTORY")
					err = CreateCatalogIntoDir(repo, dir)
					if err != nil {
						LogE(err).WithFields(log.Fields{
							"directory": dir}).Error(
							"Impossible to create subcatalog in super-directory.")
					} else {
						Log().WithFields(log.Fields{
							"directory": dir}).Info(
							"Created subcatalog in directory")
					}
				}
				err = ExecCommand("cvmfs_server", "ingest", "--catalog", "-t", "-", "-b", TrimCVMFSRepoPrefix(layerPath), repo).StdIn(layer.Path).Start()

				if err != nil {
					LogE(err).WithFields(log.Fields{"layer": layer.Name}).Error("Some error in ingest the layer")
					noErrors = false
					cleanup(TrimCVMFSRepoPrefix(layerPath))
					return
				}
				Log().WithFields(log.Fields{"layer": layer.Name}).Info("Finish Ingesting the file")
			} else {
				Log().WithFields(log.Fields{"layer": layer.Name}).Info("Skipping ingestion of layer, already exists")
			}
			//os.Remove(layer.Path)
		}
		Log().Info("Finished pushing the layers into CVMFS")
	}()
	// we create a temp directory for all the files needed, when this function finish we can remove the temp directory cleaning up
	tmpDir, err := UserDefinedTempDir("", "conversion")
	if err != nil {
		LogE(err).Error("Error in creating a temporary direcotry for all the files")
		return
	}
	defer os.RemoveAll(tmpDir)

	// this wil start to feed the above goroutine by writing into layersChanell
	err = inputImage.GetLayers(layersChanell, manifestChanell, stopGettingLayers, tmpDir)
	if err != nil {
		return err
	}

	changes, _ := inputImage.GetChanges()

	var wg sync.WaitGroup

	layerLocations := make(map[string]string)
	wg.Add(1)
	go func() {
		for layerLocation := range layerRepoLocationChan {
			layerLocations[layerLocation.Digest] = layerLocation.Location
		}
		wg.Done()
	}()

	var layerDigests []string
	wg.Add(1)
	go func() {
		for layerDigest := range layerDigestChan {
			layerDigests = append(layerDigests, layerDigest)
		}
		wg.Done()
	}()
	wg.Wait()

	thin, err := da.MakeThinImage(manifest, layerLocations, inputImage.WholeName())
	if err != nil {
		return
	}

	thinJson, err := json.MarshalIndent(thin, "", "  ")
	if err != nil {
		return
	}
	fmt.Println(string(thinJson))
	var imageTar bytes.Buffer
	tarFile := tar.NewWriter(&imageTar)
	header := &tar.Header{Name: "thin.json", Mode: 0644, Size: int64(len(thinJson))}
	err = tarFile.WriteHeader(header)
	if err != nil {
		return
	}
	_, err = tarFile.Write(thinJson)
	if err != nil {
		return
	}
	err = tarFile.Close()
	if err != nil {
		return
	}

	dockerClient, err := client.NewClientWithOpts(client.WithVersion("1.19"))
	if err != nil {
		return
	}

	image := types.ImageImportSource{
		Source:     bytes.NewBuffer(imageTar.Bytes()),
		SourceName: "-",
	}
	importOptions := types.ImageImportOptions{
		Tag:     outputImage.Tag,
		Message: "",
		Changes: changes,
	}
	importResult, err := dockerClient.ImageImport(
		context.Background(),
		image,
		outputImage.GetSimpleName(),
		importOptions)
	if err != nil {
		LogE(err).Error("Error in image import")
		return
	}
	defer importResult.Close()
	Log().Info("Created the image in the local docker daemon")

	// is necessary this mechanism to pass the authentication to the
	// dockers even if the documentation says otherwise
	authStruct := struct {
		Username string
		Password string
	}{
		Username: outputImage.User,
		Password: password,
	}
	authBytes, _ := json.Marshal(authStruct)
	authCredential := base64.StdEncoding.EncodeToString(authBytes)
	pushOptions := types.ImagePushOptions{
		RegistryAuth: authCredential,
	}

	// we wait for the goroutines to finish
	// and if there was no error we add everything to the converted table
	noErrorInConversionValue := <-noErrorInConversion

	err = SaveLayersBacklink(repo, inputImage, layerDigests)
	if err != nil {
		LogE(err).Error("Error in saving the backlinks")
		noErrorInConversionValue = false
	}

	if noErrorInConversionValue {

		res, errImgPush := dockerClient.ImagePush(
			context.Background(),
			outputImage.GetSimpleName(),
			pushOptions)
		if errImgPush != nil {
			err = fmt.Errorf("Error in pushing the image: %s", errImgPush)
			return err
		}
		b, _ := ioutil.ReadAll(res)
		fmt.Println(string(b))
		defer res.Close()
		// here is possible to use the result of the above ReadAll to have
		// informantion about the status of the upload.
		_, errReadDocker := ioutil.ReadAll(res)
		if err != nil {
			LogE(errReadDocker).Warning("Error in reading the status from docker")
		}
		Log().Info("Finish pushing the image to the registry")

		manifestPath := filepath.Join(".metadata", inputImage.GetSimpleName(), "manifest.json")
		errIng := IngestIntoCVMFS(repo, manifestPath, <-manifestChanell)
		if errIng != nil {
			LogE(errIng).Error("Error in storing the manifest in the repository")
		}
		var errRemoveSchedule error
		if alreadyConverted == ConversionNotMatch {
			Log().Info("Image already converted, but it does not match the manifest, adding it to the remove scheduler")
			errRemoveSchedule = AddManifestToRemoveScheduler(repo, manifest)
			if errRemoveSchedule != nil {
				Log().Warning("Error in adding the image to the remove schedule")
				return errRemoveSchedule
			}
		}
		if errIng == nil && errRemoveSchedule == nil {
			Log().Info("Conversion completed")
			return nil
		}
		return
	} else {
		Log().Warn("Some error during the conversion, we are not storing it into the database")
		return
	}
}

func AlreadyConverted(CVMFSRepo string, img Image, reference string) ConversionResult {
	path := filepath.Join("/", "cvmfs", CVMFSRepo, ".metadata", img.GetSimpleName(), "manifest.json")

	fmt.Println(path)
	manifestStat, err := os.Stat(path)
	if os.IsNotExist(err) {
		Log().Info("Manifest not existing")
		return ConversionNotFound
	}
	if !manifestStat.Mode().IsRegular() {
		Log().Info("Manifest not a regular file")
		return ConversionNotFound
	}

	manifestFile, err := os.Open(path)
	if err != nil {
		Log().Info("Error in opening the manifest")
		return ConversionNotFound
	}
	defer manifestFile.Close()

	bytes, _ := ioutil.ReadAll(manifestFile)

	var manifest da.Manifest
	err = json.Unmarshal(bytes, &manifest)
	if err != nil {
		LogE(err).Warning("Error in unmarshaling the manifest")
		return ConversionNotFound
	}
	fmt.Printf("%s == %s\n", manifest.Config.Digest, reference)
	if manifest.Config.Digest == reference {
		return ConversionMatch
	}
	return ConversionNotMatch
}

func GetPassword() (string, error) {
	envVar := "DUCC_DOCKER_REGISTRY_PASS"
	pass := os.Getenv(envVar)
	if pass == "" {
		err := fmt.Errorf(
			"Env variable (%s) storing the password to access the docker registry is not set", envVar)
		return "", err
	}
	return pass, nil
}
