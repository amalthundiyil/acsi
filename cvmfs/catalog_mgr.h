/**
 * This file is part of the CernVM File System.
 */

#ifndef CVMFS_CATALOG_MGR_H_
#define CVMFS_CATALOG_MGR_H_

#ifndef __STDC_FORMAT_MACROS
#define __STDC_FORMAT_MACROS
#endif

#include <inttypes.h>
#include <pthread.h>

#include <cassert>
#include <map>
#include <string>
#include <vector>

#include "catalog.h"
#include "crypto/hash.h"
#include "directory_entry.h"
#include "file_chunk.h"
#include "manifest_fetch.h"
#include "statistics.h"
#include "util/algorithm.h"
#include "util/atomic.h"
#include "util/logging.h"
#include "util/platform.h"

class XattrList;
namespace catalog {

const unsigned kSqliteMemPerThread = 1*1024*1024;


/**
 * LookupOption for a directory entry (bitmask).
 * kLookupDefault = Look solely at the given directory entry (parent is ignored)
 * kLookupRawSymlink = Don't resolve environment variables in symlink targets
 */
typedef unsigned LookupOptions;
const unsigned kLookupDefault = 0b1;
const unsigned kLookupRawSymlink = 0b10;

/**
 * Results upon loading a catalog file.
 */
enum LoadReturn {
  kLoadNew = 0,
  kLoadUp2Date,
  kLoadNoSpace,
  kLoadFail,

  kLoadNumEntries
};

/**
 * Location of the most recent root catalog.
 * Used as part of the process of loading a catalog.
 * - GetNewRootCatalogContext() sets the location within the CatalogContext obj
 * - LoadCatalogByHash(): when loading a root catalog it uses the location
 *                        stored within the CatalogContext object to retrieve
 *                        the root catalog from the right location
 */
enum RootCatalogLocation {
  kCtlgNoLocationNeeded = 0,  // hash known, no location needed
  kCtlgLocationMounted,      // already loaded in mounted_catalogs_
  kCtlgLocationServer,
  kCtlgLocationBreadcrumb
};

/**
 * CatalogContext class contains all necessary information to load a catalog and
 * also keeps track of the resulting output.
 * It works as follows:
 * 1) Load a new root catalog:
 *  - Use empty constructor CatalogContext()
 *  - Let the CatalogContext object be populated by GetNewRootCatalogContext()
 *    - This will set: hash, mountpoint, root_ctlg_revision, root_ctlg_location
 *  - Call LoadCatalogByHash()
 *    - This will set: sqlite_path
 * 2) Load a catalog based on a given hash
 *  - Populate CatalogContext object; used constructor depends on catalog type
 *    - Root catalog: CatalogContext(shash::Any hash, PathString mountpoint,
              RootCatalogLocation location)
      - Nested catalog: CatalogContext(shash::Any hash, PathString mountpoint)
      - Note: in this case root_ctlg_revision is not used
 *  - Call LoadCatalogByHash()
      - This will set: sqlite_path
 */
struct CatalogContext {
 public:
  CatalogContext() :
              hash_(shash::Any()),
              mountpoint_(PathString("invalid", 7)),  // empty str is root ctlg
              sqlite_path_(""),
              root_ctlg_revision_(-1ul),
              root_ctlg_location_(kCtlgNoLocationNeeded),
              manifest_ensemble_(NULL) { }
  CatalogContext(const shash::Any &hash, const PathString &mountpoint) :
              hash_(hash),
              mountpoint_(mountpoint),
              sqlite_path_(""),
              root_ctlg_revision_(-1ul),
              root_ctlg_location_(kCtlgNoLocationNeeded),
              manifest_ensemble_(NULL) { }

  CatalogContext(const shash::Any &hash, const PathString &mountpoint,
                 const RootCatalogLocation location) :
              hash_(hash),
              mountpoint_(mountpoint),
              sqlite_path_(""),
              root_ctlg_revision_(-1ul),
              root_ctlg_location_(location),
              manifest_ensemble_(NULL)  { }

  bool IsRootCatalog() {
    return mountpoint_.IsEmpty();
  }

  std::string *GetSqlitePathPtr() { return &sqlite_path_; }
  shash::Any *GetHashPtr() { return &hash_; }

  shash::Any hash() const { return hash_; }
  PathString mountpoint() const { return mountpoint_; }
  std::string sqlite_path() const { return sqlite_path_; }
  uint64_t root_ctlg_revision() const { return root_ctlg_revision_; }
  RootCatalogLocation root_ctlg_location() const
                                                 { return root_ctlg_location_; }
  manifest::ManifestEnsemble *manifest_ensemble() const
                                       { return manifest_ensemble_.weak_ref(); }

  void SetHash(shash::Any hash) { hash_ = hash; }
  void SetMountpoint(const PathString &mountpoint) { mountpoint_ = mountpoint; }
  void SetSqlitePath(const std::string &sqlite_path)
                                                 { sqlite_path_ = sqlite_path; }
  void SetRootCtlgRevision(uint64_t root_ctlg_revision)
                                   { root_ctlg_revision_ = root_ctlg_revision; }
  void SetRootCtlgLocation(RootCatalogLocation root_ctlg_location)
                                   { root_ctlg_location_ = root_ctlg_location; }
  /**
   * Gives ownership to CatalogContext
   */
  void TakeManifestEnsemble(manifest::ManifestEnsemble *manifest_ensemble)
                                     { manifest_ensemble_ = manifest_ensemble; }


 private:
  // mandatory for LoadCatalogByHash()
  shash::Any hash_;
  // mandatory for LoadCatalogByHash()
  PathString mountpoint_;
  // out parameter, path name of the sqlite catalog
  std::string sqlite_path_;
  // root catalog: revision is needed for GetNewRootCatalogContext()
  uint64_t root_ctlg_revision_;
  // root catalog: location is mandatory for LoadCatalogByHash()
  RootCatalogLocation root_ctlg_location_;
  // root catalog: if location = server mandatory for LoadCatalogByHash()
  UniquePtr<manifest::ManifestEnsemble> manifest_ensemble_;
};

inline const char *Code2Ascii(const LoadReturn error) {
  const char *texts[kLoadNumEntries + 1];
  texts[0] = "loaded new catalog";
  texts[1] = "catalog was up to date";
  texts[2] = "not enough space to load catalog";
  texts[3] = "failed to load catalog";
  texts[4] = "no text";
  return texts[error];
}


struct Statistics {
  perf::Counter *n_lookup_inode;
  perf::Counter *n_lookup_path;
  perf::Counter *n_lookup_path_negative;
  perf::Counter *n_lookup_xattrs;
  perf::Counter *n_listing;
  perf::Counter *n_nested_listing;
  perf::Counter *n_detach_siblings;
  perf::Counter *n_write_lock;
  perf::Counter *ns_write_lock;

  perf::Counter *catalog_revision;

  explicit Statistics(perf::Statistics *statistics) {
    n_lookup_inode = statistics->Register("catalog_mgr.n_lookup_inode",
        "Number of inode lookups");
    n_lookup_path = statistics->Register("catalog_mgr.n_lookup_path",
        "Number of path lookups");
    n_lookup_path_negative = statistics->Register(
        "catalog_mgr.n_lookup_path_negative",
        "Number of negative path lookups");
    n_lookup_xattrs = statistics->Register("catalog_mgr.n_lookup_xattrs",
        "Number of xattrs lookups");
    n_listing = statistics->Register("catalog_mgr.n_listing",
        "Number of listings");
    n_nested_listing = statistics->Register("catalog_mgr.n_nested_listing",
        "Number of listings of nested catalogs");
    n_detach_siblings = statistics->Register("catalog_mgr.n_detach_siblings",
        "Number of times the CVMFS_CATALOG_WATERMARK was hit");
    n_write_lock = statistics->Register("catalog_mgr.n_write_lock",
                                        "number of write lock calls");
    ns_write_lock = statistics->Register("catalog_mgr.ns_write_lock",
        "time spent in WriteLock() [ns]");
    catalog_revision = statistics->Register("catalog_revision",
                                    "Revision number of the root file catalog");
  }
};


template <class CatalogT>
class AbstractCatalogManager;


/**
 * This class provides the read-only interface to a tree of catalogs
 * representing a (subtree of a) repository.
 * Mostly lookup functions filling DirectoryEntry objects.
 * Reloading of expired catalogs, attaching of nested catalogs and delegating
 * of lookups to the appropriate catalog is done transparently.
 *
 * The loading / creating of catalogs is up to derived classes.
 *
 * CatalogT is either Catalog or MockCatalog.
 *
 * Usage:
 *   DerivedCatalogManager *catalog_manager = new DerivedCatalogManager();
 *   catalog_manager->Init();
 *   catalog_manager->Lookup(<inode>, &<result_entry>);
 */
template <class CatalogT>
class AbstractCatalogManager : public SingleCopy {
 public:
  typedef std::vector<CatalogT*> CatalogList;
  typedef CatalogT catalog_t;

  static const inode_t kInodeOffset = 255;
  explicit AbstractCatalogManager(perf::Statistics *statistics);
  virtual ~AbstractCatalogManager();

  void SetInodeAnnotation(InodeAnnotation *new_annotation);
  virtual bool Init();
  LoadReturn RemountDryrun();
  LoadReturn Remount();
  LoadReturn ChangeRoot(const shash::Any &root_hash);
  void DetachNested();

  bool LookupPath(const PathString &path, const LookupOptions options,
                  DirectoryEntry *entry);
  bool LookupPath(const std::string &path, const LookupOptions options,
                  DirectoryEntry *entry)
  {
    PathString p;
    p.Assign(&path[0], path.length());
    return LookupPath(p, options, entry);
  }
  bool LookupXattrs(const PathString &path, XattrList *xattrs);

  bool LookupNested(const PathString &path,
                    PathString *mountpoint,
                    shash::Any *hash,
                    uint64_t *size);
  bool ListCatalogSkein(const PathString &path,
                        std::vector<PathString> *result_list);

  bool Listing(const PathString &path, DirectoryEntryList *listing,
               const bool expand_symlink);
  bool Listing(const PathString &path, DirectoryEntryList *listing) {
    return Listing(path, listing, true);
  }
  bool Listing(const std::string &path, DirectoryEntryList *listing) {
    PathString p;
    p.Assign(&path[0], path.length());
    return Listing(p, listing);
  }
  bool ListingStat(const PathString &path, StatEntryList *listing);

  bool ListFileChunks(const PathString &path,
                      const shash::Algorithms interpret_hashes_as,
                      FileChunkList *chunks);
  void SetOwnerMaps(const OwnerMap &uid_map, const OwnerMap &gid_map);
  void SetCatalogWatermark(unsigned limit);

  shash::Any GetNestedCatalogHash(const PathString &mountpoint);

  Statistics statistics() const { return statistics_; }
  uint64_t inode_gauge() {
    ReadLock(); uint64_t r = inode_gauge_; Unlock(); return r;
  }
  bool volatile_flag() const { return volatile_flag_; }
  uint64_t GetRevision() const;
  uint64_t GetTimestamp() const;
  uint64_t GetTTL() const;
  bool HasExplicitTTL() const;
  bool GetVOMSAuthz(std::string *authz) const;
  int GetNumCatalogs() const;
  std::string PrintHierarchy() const;
  std::string PrintAllMemStatistics() const;

  /**
   * Get the inode number of the root DirectoryEntry
   * ('root' means the root of the whole file system)
   * @return the root inode number
   */
  inline inode_t GetRootInode() const {
    return inode_annotation_ ?
      inode_annotation_->Annotate(kInodeOffset + 1) : kInodeOffset + 1;
  }
  inline CatalogT* GetRootCatalog() const { return catalogs_.front(); }
  /**
   * Inodes are ambiquitous under some circumstances, to prevent problems
   * they must be passed through this method first
   * @param inode the raw inode
   * @return the revised inode
   */
  inline inode_t MangleInode(const inode_t inode) const {
    return (inode <= kInodeOffset) ? GetRootInode() : inode;
  }

  catalog::Counters LookupCounters(const PathString &path,
                                   std::string *subcatalog_path,
                                   shash::Any *hash);

 protected:
  /**
   * Load the catalog and return a file name and the catalog hash.
   *
   * GetNewRootCatalogContext() populates CatalogContext object with the
   * information needed to retrieve the most recent root catalog independent of
   * its location.
   * The CatalogContext object must be populated with at least hash and
   * mountpoint to call LoadCatalogByHash().
   *
   * See class description of CatalogContext for more information.
   */
  virtual LoadReturn GetNewRootCatalogContext(CatalogContext *result) = 0;
  virtual LoadReturn LoadCatalogByHash(CatalogContext *ctlg_context) = 0;
  virtual void UnloadCatalog(const CatalogT *catalog) { }
  virtual void ActivateCatalog(CatalogT *catalog) { }
  const std::vector<CatalogT*>& GetCatalogs() const { return catalogs_; }

  /**
   * Opportunistic optimization: the client catalog manager uses this method
   * to preload into the cache a nested catalog that is likely to be required
   * next. Likely, because there is a race with the root catalog reload which
   * may result in the wrong catalog being staged. That's not a fault though,
   * the correct catalog will still be loaded with the write lock held.
   * Note that this method is never used for root catalogs.
   */
  virtual void StageNestedCatalogByHash(const shash::Any & /*hash*/,
                                        const PathString & /*mountpoint*/)
  { }
  /**
   * Called within the ReadLock(), which will be released before downloading
   * the catalog (and before leaving the method)
   */
  void StageNestedCatalogAndUnlock(const PathString &path,
                                   const CatalogT *parent,
                                   bool is_listable);

  /**
   * Create a new Catalog object.
   * Every derived class has to implement this and return a newly
   * created (derived) Catalog structure of it's desired type.
   * @param mountpoint      the future mountpoint of the catalog to create
   * @param catalog_hash    the content hash of the catalog database
   * @param parent_catalog  the parent of the catalog to create
   * @return a newly created (derived) Catalog
   */
  virtual CatalogT* CreateCatalog(const PathString  &mountpoint,
                                  const shash::Any  &catalog_hash,
                                  CatalogT *parent_catalog) = 0;

  CatalogT *MountCatalog(const PathString &mountpoint, const shash::Any &hash,
                         CatalogT *parent_catalog);
  bool MountSubtree(const PathString &path,
                    const CatalogT *entry_point,
                    bool can_listing,
                    CatalogT **leaf_catalog);

  CatalogT *LoadFreeCatalog(const PathString &mountpoint,
                            const shash::Any &hash);

  bool AttachCatalog(const std::string &db_path, CatalogT *new_catalog);
  void DetachCatalog(CatalogT *catalog);
  void DetachSubtree(CatalogT *catalog);
  void DetachSiblings(const PathString &current_tree);
  void DetachAll() { if (!catalogs_.empty()) DetachSubtree(GetRootCatalog()); }
  bool IsAttached(const PathString &root_path,
                  CatalogT **attached_catalog) const;

  CatalogT *FindCatalog(const PathString &path) const;

  uint64_t GetRevisionNoLock() const;
  uint64_t GetTimestampNoLock() const;
  inline void ReadLock() const {
    int retval = pthread_rwlock_rdlock(rwlock_);
    assert(retval == 0);
  }
  inline void WriteLock() const {
    uint64_t timestamp = platform_monotonic_time_ns();
    int retval = pthread_rwlock_wrlock(rwlock_);
    assert(retval == 0);
    perf::Inc(statistics_.n_write_lock);
    uint64_t duration = platform_monotonic_time_ns() - timestamp;
    perf::Xadd(statistics_.ns_write_lock, duration);
  }
  inline void Unlock() const {
    int retval = pthread_rwlock_unlock(rwlock_);
    assert(retval == 0);
  }
  virtual void EnforceSqliteMemLimit();

 private:
  void CheckInodeWatermark();

  /**
   * The flat list of all attached catalogs.
   */
  CatalogList catalogs_;
  int inode_watermark_status_;  /**< 0: OK, 1: > 32bit */
  uint64_t inode_gauge_;  /**< highest issued inode */
  uint64_t revision_cache_;
  uint64_t timestamp_cache_;
  /**
   * Try to keep number of nested catalogs below the given limit. Zero means no
   * limit. Surpassing the watermark on mounting a catalog triggers
   * a DetachSiblings() call.
   */
  unsigned catalog_watermark_;
  /**
   * Not protected by a read lock because it can only change when the root
   * catalog is exchanged (during big global lock of the file system).
   */
  bool volatile_flag_;
  /**
   * Saves the result of GetVOMSAuthz when a root catalog is attached
   */
  bool has_authz_cache_;
  /**
   * Saves the VOMS requirements when a root catalog is attached
   */
  std::string authz_cache_;
  /**
   * Counts how often the inodes have been invalidated.
   */
  uint64_t incarnation_;
  // TODO(molina) we could just add an atomic global counter instead
  InodeAnnotation *inode_annotation_;  /**< applied to all catalogs */
  pthread_rwlock_t *rwlock_;
  Statistics statistics_;
  pthread_key_t pkey_sqlitemem_;
  OwnerMap uid_map_;
  OwnerMap gid_map_;

  // Not needed anymore since there are the glue buffers
  // Catalog *Inode2Catalog(const inode_t inode);
  std::string PrintHierarchyRecursively(const CatalogT *catalog,
                                        const int level) const;
  std::string PrintMemStatsRecursively(const CatalogT *catalog) const;

  InodeRange AcquireInodes(uint64_t size);
  void ReleaseInodes(const InodeRange chunk);
};  // class CatalogManager

class InodeGenerationAnnotation : public InodeAnnotation {
 public:
  InodeGenerationAnnotation() { inode_offset_ = 0; }
  virtual ~InodeGenerationAnnotation() { }
  virtual bool ValidInode(const uint64_t inode) {
    return inode >= inode_offset_;
  }
  virtual inode_t Annotate(const inode_t raw_inode) {
    return raw_inode + inode_offset_;
  }
  virtual inode_t Strip(const inode_t annotated_inode) {
    return annotated_inode - inode_offset_;
  }
  virtual void IncGeneration(const uint64_t by) {
    inode_offset_ += by;
    LogCvmfs(kLogCatalog, kLogDebug, "set inode generation to %lu",
             inode_offset_);
  }
  virtual inode_t GetGeneration() { return inode_offset_; }

 private:
  uint64_t inode_offset_;
};

/**
 * In NFS mode, the root inode has to be always 256. Otherwise the inode maps
 * lookup fails. In general, the catalog manager inodes in NFS mode are only
 * used for the chunk tables.
 */
class InodeNfsGenerationAnnotation : public InodeAnnotation {
 public:
  InodeNfsGenerationAnnotation() { inode_offset_ = 0; }
  virtual ~InodeNfsGenerationAnnotation() { }
  virtual bool ValidInode(const uint64_t inode) {
    return (inode >= inode_offset_) || (inode == kRootInode);
  }
  virtual inode_t Annotate(const inode_t raw_inode) {
    if (raw_inode <= kRootInode)
      return kRootInode;
    return raw_inode + inode_offset_;
  }
  virtual inode_t Strip(const inode_t annotated_inode) {
    if (annotated_inode == kRootInode)
      return annotated_inode;
    return annotated_inode - inode_offset_;
  }
  virtual void IncGeneration(const uint64_t by) {
    inode_offset_ += by;
    LogCvmfs(kLogCatalog, kLogDebug, "set inode generation to %lu",
             inode_offset_);
  }
  virtual inode_t GetGeneration() { return inode_offset_; }

 private:
  static const uint64_t kRootInode =
    AbstractCatalogManager<Catalog>::kInodeOffset + 1;
  uint64_t inode_offset_;
};

}  // namespace catalog

#include "catalog_mgr_impl.h"

#endif  // CVMFS_CATALOG_MGR_H_
