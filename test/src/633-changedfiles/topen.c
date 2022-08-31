#include <sys/xattr.h>  // NOLINT

#include <fcntl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#include <stdio.h>
#include <stdlib.h>

static int Exists(char *path) {
  struct stat info;
  return stat(path, &info) == 0;
}

static unsigned ChecksumStep(char c, unsigned cksum) {
  unsigned p = 0x04C11DB7;
  for (int i = 7; i >= 0; i--) {
    int msb = cksum & (1 << 31);
    cksum <<= 1;
    if (msb)
      cksum = cksum ^ p;
  }
  cksum ^= c;
  return cksum;
}
static unsigned InitChecksum() { return 0; }
static unsigned UpdateChecksum(char *buf, unsigned len, unsigned cksum) {
  for (unsigned i = 0; i < len; ++i) {
    cksum = ChecksumStep(buf[i], cksum);
  }
  return cksum;
}
static unsigned FinalizeChecksum(int buflen, unsigned cksum) {
  do {
    cksum = ChecksumStep(buflen & 0xff, cksum);
    buflen >>= 8;
  } while (buflen);

  cksum = ChecksumStep(0, cksum);
  cksum = ChecksumStep(0, cksum);
  cksum = ChecksumStep(0, cksum);
  cksum = ChecksumStep(0, cksum);
  return ~cksum;
}

int main(int argc, char **argv) {
  if (argc < 2) {
    fprintf(stderr, "Usage: %s /cvmfs/<path>\n", argv[0]);
    return 1;
  }

  char *path = argv[1];
  int fd = open(path, O_RDONLY);
  if (fd < 0) {
    fprintf(stderr, "cannot open %s\n", path);
    return 1;
  }

  int round = 0;
  while (!Exists("stop_topen")) {
    char xattr_revision[64];
    int rv = getxattr(path, "user.revision", xattr_revision, 63);
    int revision = -1;
    if (rv >= 0) {
      xattr_revision[rv] = '\0';
      revision = atoi(xattr_revision);
    } else {
      fprintf(stderr, "cannot get revision attribute (%s)\n", path);
      return 1;
    }

    struct stat info;
    rv = fstat(fd, &info);
    if (rv < 0) {
      fprintf(stderr, "cannot fstat %s\n", path);
      return 1;
    }

    off_t off = lseek(fd, 0, SEEK_SET);
    if (off != 0) {
      fprintf(stderr, "cannot rewind %s\n", path);
      return 1;
    }
    char buf[4096];
    unsigned cksum_read = InitChecksum();
    int len = 0;
    do {
      rv = read(fd, buf, 4096);
      if (rv < 0) {
        fprintf(stderr, "cannot read %s\n", path);
        return 1;
      }
      cksum_read = UpdateChecksum(buf, rv, cksum_read);
      len += rv;
    } while (rv == 4096);
    cksum_read = FinalizeChecksum(len, cksum_read);

    void *mapped = mmap(NULL, info.st_size, PROT_READ, MAP_PRIVATE, fd, 0);
    if (mapped == MAP_FAILED) {
      fprintf(stderr, "cannot mmap %s\n", path);
      return 1;
    }
    unsigned cksum_mmap = InitChecksum();
    cksum_mmap = UpdateChecksum(mapped, info.st_size, cksum_mmap);
    cksum_mmap = FinalizeChecksum(info.st_size, cksum_mmap);
    munmap(mapped, info.st_size);

    printf("**** ROUND %d [%s]\n", round, path);
    printf("Revision: %d\n", revision);
    printf("Length: %lu\n", info.st_size);
    printf("Inode: %lu\n", info.st_ino);
    printf("Len(read): %d\n", len);
    printf("Cksum(read): %u\n", cksum_read);
    printf("Cksum(mmap): %u\n", cksum_mmap);

    sleep(1);
    round++;
  }

  printf("*** END %s %s\n", argv[0], path);

  return 0;
}
