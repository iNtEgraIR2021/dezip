package main

import (
    "fmt"
    "log"
    "os"
    "path"
    "runtime/debug"
    "syscall"
    "time"
)

// reclamation begins when free space dips below the lowWaterMark and continues
// until free space exceeds the highWaterMark.
const lowWaterMark = 5_000_000_000 // 5 GB
const highWaterMark = 10_000_000_000 // 10 GB

const reclamationInterval = 1 * time.Second

func (c *cache) reclaim(archiveURL string) (err error) {
    ar := c.archivesByURL[archiveURL]
    if ar == nil {
        err = fmt.Errorf("couldn't find archive for URL %s", archiveURL)
        return
    }
    // remove the archive from the reclamation list and the archivesByURL map.
    c.mutex.Lock()
    delete(c.archivesByURL, archiveURL)
    for i, v := range c.archiveURLsToReclaim {
        if v == archiveURL {
            if i == 0 {
                c.archiveURLsToReclaim = c.archiveURLsToReclaim[1:]
            } else {
                c.archiveURLsToReclaim = append(c.archiveURLsToReclaim[:i], c.archiveURLsToReclaim[i+1:]...)
            }
            break
        }
    }
    c.mutex.Unlock()

    ar.mutex.Lock()
    ar.transitionToState(archiveStateFailed)
    ar.failureReason = fmt.Errorf("this archive is being reclaimed")
    ar.mutex.Unlock()

    log.Printf("reclaiming archive %s", ar.path)
    c.reclaimFiles(ar.path)

    return
}

func (c *cache) reclaimLoop() {
    for {
        err := func () (err error) {
            defer func () {
                if r := recover(); r != nil {
                    err = fmt.Errorf("panic during reclaimIfNeeded: %v\n%v", r, string(debug.Stack()))
                }
            }()
            var bytes uint64
            if bytes, err = availableBytes(c.rootPath); err != nil {
                return
            }
            if bytes > lowWaterMark {
                return
            }
            for bytes < highWaterMark {
                if len(c.archiveURLsToReclaim) == 0 {
                    err = fmt.Errorf("still low on space, even after reclaiming all archives")
                    return
                }
                if err = c.reclaim(c.archiveURLsToReclaim[0]); err != nil {
                    return
                }
                if bytes, err = availableBytes(c.rootPath); err != nil {
                    return
                }
            }
            return
        }()
        if err != nil {
            log.Print(err)
        }
        time.Sleep(reclamationInterval)
    }
}

func availableBytes(path string) (uint64, error) {
    var s syscall.Statfs_t
    if err := syscall.Statfs(path, &s); err != nil {
        return 0, err
    }
    return uint64(s.Bsize) * s.Bavail, nil
}

func (c *cache) reclaimFiles(archivePath string) {
    os.Remove(c.archiveMetadataPath(archivePath))
    reclaimDirectory(path.Join(c.rootPath, archivePath))
    reclaimDirectory(path.Join(c.textPath, archivePath))
}

func reclaimDirectory(directory string) {
    os.RemoveAll(directory)
    // reclaim empty parent directories.
    for {
        directory = path.Dir(directory)
        if err := os.Remove(directory); err != nil {
            break
        }
    }
}
