"""A read-only FUSE filesystem for testing the transport without cloud services."""
import errno
import stat
import sys
import time

from fusepy import FUSE, FuseOSError, Operations


class DemoFS(Operations):
    def __init__(self, source):
        self.data = ("hello from " + source + "\n").encode()
        self.created = time.time()

    def getattr(self, path, fh=None):
        if path == "/":
            mode, size, links = stat.S_IFDIR | 0o555, 0, 2
        elif path == "/hello.txt":
            mode, size, links = stat.S_IFREG | 0o444, len(self.data), 1
        else:
            raise FuseOSError(errno.ENOENT)
        return dict(st_mode=mode, st_size=size, st_nlink=links, st_uid=0,
                    st_gid=0, st_atime=self.created, st_mtime=self.created,
                    st_ctime=self.created)

    def readdir(self, path, fh):
        return [".", "..", "hello.txt"]

    def open(self, path, flags):
        if path != "/hello.txt":
            raise FuseOSError(errno.ENOENT)
        return 0

    def read(self, path, size, offset, fh):
        if path != "/hello.txt":
            raise FuseOSError(errno.ENOENT)
        return self.data[offset:offset + size]


if __name__ == "__main__":
    if sys.argv[2] == "fail":
        sys.exit("intentional demo mount failure")
    FUSE(DemoFS(sys.argv[2]), sys.argv[1], foreground=True,
         allow_other=True, ro=True, nothreads=True)
