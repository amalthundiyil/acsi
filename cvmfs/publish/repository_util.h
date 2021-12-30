/**
 * This file is part of the CernVM File System.
 *
 * Private utility functions for libcvmfs_server
 */

#ifndef CVMFS_PUBLISH_REPOSITORY_UTIL_H_
#define CVMFS_PUBLISH_REPOSITORY_UTIL_H_

#include <string>

#include "hash.h"

namespace publish {

class CheckoutMarker {
 public:
  CheckoutMarker(
    const std::string &t,
    const std::string &b,
    const shash::Any &h,
    const std::string &p)
    : tag_(t), branch_(b), hash_(h), previous_branch_(p)
  {}

  static CheckoutMarker *CreateFrom(const std::string &path);
  void SaveAs(const std::string &path) const;

  std::string tag() const { return tag_; }
  std::string branch() const { return branch_; }
  shash::Any hash() const { return hash_; }
  std::string previous_branch() const { return previous_branch_; }

 private:
  std::string tag_;
  std::string branch_;
  shash::Any hash_;
  std::string previous_branch_;
};


/**
 * The server lock file is a file containing the pid of the creator, so that
 * with high probability one can determine stale locks.  This comes from the
 * cvmfs_server bash times and should at some point become a regular POSIX
 * lock file.
 */
class ServerLockFile {
 public:
  explicit ServerLockFile(const std::string &path) : path_(path) {}

  bool Acquire();
  void Release();
  bool IsLocked() const;

  const std::string &path() const { return path_; }

 private:
  std::string path_;
};

/**
 * A server flag file is a file used to indicate a single-bit state
 * that extends beyond the lifetime of a process, such as the
 * indication that a transaction is open.
 */
class ServerFlagFile {
 public:
  explicit ServerFlagFile(const std::string &path) : path_(path) {}

  void Set();
  void Clear();
  bool IsSet() const;

  const std::string &path() const { return path_; }

 private:
  std::string path_;
};

/**
 * Callout to cvmfs_suid_helper $verb $fqrn
 */
void RunSuidHelper(const std::string &verb, const std::string &fqrn);


/**
 * Replaces or creates $key=$value in the config file $path. Creates $path
 * if necessary. If value is empty, the key is removed.
 */
void SetInConfig(const std::string &path,
                 const std::string &key, const std::string &value);

std::string SendTalkCommand(const std::string &socket, const std::string &cmd);

}  // namespace publish

#endif  // CVMFS_PUBLISH_REPOSITORY_UTIL_H_
