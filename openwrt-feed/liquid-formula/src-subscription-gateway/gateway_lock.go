package main

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
)

var (
	errSubscriptionBusy        = errors.New("subscription lock busy")
	errSubscriptionLockInvalid = errors.New("subscription lock invalid")
)

const subscriptionBarrierSuffix = ".barrier"

type fileSubscriptionLocker struct {
	path              string
	retry             time.Duration
	afterOpenTestHook func()
}

func newFileSubscriptionLocker(
	path string,
	retry time.Duration,
) *fileSubscriptionLocker {
	return &fileSubscriptionLocker{path: path, retry: retry}
}

func (locker *fileSubscriptionLocker) Acquire(
	ctx context.Context,
) (heldSubscriptionLock, error) {
	if locker == nil || locker.path == "" || locker.retry <= 0 {
		return nil, errSubscriptionLockInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, errSubscriptionBusy
	}
	file, err := openSubscriptionLockFile(locker.path)
	if err != nil {
		return nil, errSubscriptionLockInvalid
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	if locker.afterOpenTestHook != nil {
		locker.afterOpenTestHook()
	}
	if ctx.Err() != nil {
		return nil, errSubscriptionBusy
	}
	if !validSubscriptionLockFD(file) {
		return nil, errSubscriptionLockInvalid
	}

	for {
		if ctx.Err() != nil {
			return nil, errSubscriptionBusy
		}
		err = syscall.Flock(
			int(file.Fd()),
			syscall.LOCK_EX|syscall.LOCK_NB,
		)
		if err == nil {
			if ctx.Err() != nil {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				return nil, errSubscriptionBusy
			}
			if !validSubscriptionLockFD(file) ||
				!subscriptionLockPathMatches(file, locker.path) {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				return nil, errSubscriptionLockInvalid
			}
			keep = true
			return &fileHeldSubscriptionLock{file: file}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, errSubscriptionLockInvalid
		}
		if validSubscriptionBarrier(
			file, locker.path+subscriptionBarrierSuffix,
		) {
			return nil, errSubscriptionBusy
		}
		if !waitForSubscriptionLockRetry(ctx, locker.retry) {
			return nil, errSubscriptionBusy
		}
	}
}

func validSubscriptionBarrier(lockFile *os.File, path string) bool {
	flags := syscall.O_RDONLY |
		syscall.O_CLOEXEC |
		syscall.O_NOFOLLOW |
		syscall.O_NONBLOCK
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return false
	}
	barrier := os.NewFile(uintptr(fd), path)
	if barrier == nil {
		_ = syscall.Close(fd)
		return false
	}
	defer barrier.Close()
	if !validSubscriptionBarrierFD(barrier, lockFile) ||
		!subscriptionLockPathMatches(barrier, path) {
		return false
	}
	body := make([]byte, 4)
	count, readErr := barrier.Read(body)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false
	}
	return count == 3 && string(body[:count]) == "v1\n"
}

func validSubscriptionBarrierFD(
	barrier *os.File,
	lockFile *os.File,
) bool {
	if barrier == nil || lockFile == nil {
		return false
	}
	var barrierStat syscall.Stat_t
	if err := syscall.Fstat(int(barrier.Fd()), &barrierStat); err != nil {
		return false
	}
	var lockStat syscall.Stat_t
	if err := syscall.Fstat(int(lockFile.Fd()), &lockStat); err != nil {
		return false
	}
	return barrierStat.Mode&syscall.S_IFMT == syscall.S_IFREG &&
		barrierStat.Mode&07777 == 0600 &&
		barrierStat.Nlink == 1 &&
		barrierStat.Size == 3 &&
		barrierStat.Uid == lockStat.Uid
}

func openSubscriptionLockFile(path string) (*os.File, error) {
	flags := syscall.O_RDWR |
		syscall.O_CLOEXEC |
		syscall.O_NOFOLLOW |
		syscall.O_NONBLOCK
	for attempt := 0; attempt < 3; attempt++ {
		fd, err := syscall.Open(
			path,
			flags|syscall.O_CREAT|syscall.O_EXCL,
			0600,
		)
		if err == nil {
			file := os.NewFile(uintptr(fd), path)
			if file == nil {
				_ = syscall.Close(fd)
				return nil, errSubscriptionLockInvalid
			}
			if err := file.Chmod(0600); err != nil {
				_ = file.Close()
				return nil, errSubscriptionLockInvalid
			}
			return file, nil
		}
		if err != syscall.EEXIST {
			return nil, err
		}
		fd, err = syscall.Open(path, flags, 0)
		if err == syscall.ENOENT {
			continue
		}
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), path)
		if file == nil {
			_ = syscall.Close(fd)
			return nil, errSubscriptionLockInvalid
		}
		return file, nil
	}
	return nil, errSubscriptionLockInvalid
}

func validSubscriptionLockFD(file *os.File) bool {
	if file == nil {
		return false
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return false
	}
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG &&
		stat.Mode&07777 == 0600 &&
		stat.Nlink == 1
}

func subscriptionLockPathMatches(file *os.File, path string) bool {
	var opened syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &opened); err != nil {
		return false
	}
	var current syscall.Stat_t
	if err := syscall.Lstat(path, &current); err != nil {
		return false
	}
	return current.Mode&syscall.S_IFMT == syscall.S_IFREG &&
		current.Mode&07777 == 0600 &&
		current.Nlink == 1 &&
		current.Dev == opened.Dev &&
		current.Ino == opened.Ino
}

func waitForSubscriptionLockRetry(
	ctx context.Context,
	retry time.Duration,
) bool {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= retry {
			return false
		}
	}
	timer := time.NewTimer(retry)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type fileHeldSubscriptionLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (lock *fileHeldSubscriptionLock) Release() error {
	if lock == nil {
		return errSubscriptionLockInvalid
	}
	lock.once.Do(func() {
		if lock.file == nil {
			lock.err = errSubscriptionLockInvalid
			return
		}
		if err := syscall.Flock(
			int(lock.file.Fd()), syscall.LOCK_UN,
		); err != nil {
			lock.err = errSubscriptionLockInvalid
		}
		if err := lock.file.Close(); err != nil && lock.err == nil {
			lock.err = errSubscriptionLockInvalid
		}
	})
	return lock.err
}
