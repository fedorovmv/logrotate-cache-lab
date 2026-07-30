package cgroup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Version string

const (
	VersionV1 Version = "v1"
	VersionV2 Version = "v2"
)

type MemorySample struct {
	Current      uint64
	Cache        uint64
	Anon         *uint64
	InactiveFile *uint64
	ActiveFile   *uint64
	Dirty        *uint64
	Writeback    *uint64
	Shmem        *uint64
	Version      Version
	CacheSource  string
}

type Reader struct {
	root    string
	version Version
}

func NewReader(root string, version Version) *Reader {
	return &Reader{root: root, version: version}
}

func Discover() (*Reader, error) {
	if _, err := os.Stat("/sys/fs/cgroup/memory.current"); err == nil {
		return NewReader("/sys/fs/cgroup", VersionV2), nil
	}
	for _, root := range []string{"/sys/fs/cgroup/memory", "/sys/fs/cgroup"} {
		if _, err := os.Stat(filepath.Join(root, "memory.usage_in_bytes")); err == nil {
			return NewReader(root, VersionV1), nil
		}
	}
	return nil, errors.New("no supported cgroup memory controller found")
}

func (r *Reader) Sample() (MemorySample, error) {
	usageName := "memory.current"
	if r.version == VersionV1 {
		usageName = "memory.usage_in_bytes"
	}
	current, err := readUint(filepath.Join(r.root, usageName))
	if err != nil {
		return MemorySample{}, err
	}
	stats, err := readStats(filepath.Join(r.root, "memory.stat"))
	if err != nil {
		return MemorySample{}, err
	}
	cacheSource := "file"
	cache, ok := stats[cacheSource]
	if r.version == VersionV1 {
		cacheSource = "total_cache"
		cache, ok = stats[cacheSource]
		if !ok {
			cacheSource = "cache"
			cache, ok = stats[cacheSource]
		}
	}
	if !ok {
		return MemorySample{}, fmt.Errorf("cache counter not found in memory.stat")
	}
	return MemorySample{
		Current:      current,
		Cache:        cache,
		Anon:         statPtr(stats, "anon", "total_rss"),
		InactiveFile: statPtr(stats, "inactive_file", "total_inactive_file"),
		ActiveFile:   statPtr(stats, "active_file", "total_active_file"),
		Dirty:        statPtr(stats, "file_dirty", "dirty"),
		Writeback:    statPtr(stats, "file_writeback", "writeback"),
		Shmem:        statPtr(stats, "shmem", "total_shmem"),
		Version:      r.version,
		CacheSource:  cacheSource,
	}, nil
}

func (r *Reader) RSSFromCgroupProcs() (uint64, error) {
	path := filepath.Join(r.root, "cgroup.procs")
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var total uint64
	s := bufio.NewScanner(f)
	for s.Scan() {
		pid := strings.TrimSpace(s.Text())
		if pid == "" {
			continue
		}
		rss, err := readVmRSS(filepath.Join("/proc", pid, "status"))
		if err == nil {
			total += rss
		}
	}
	return total, s.Err()
}

func readVmRSS(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) >= 2 && fields[0] == "VmRSS:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			return kb * 1024, err
		}
	}
	return 0, errors.New("VmRSS not found")
}

func readUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}

func readStats(path string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := make(map[string]uint64)
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s %s: %w", path, fields[0], err)
		}
		out[fields[0]] = v
	}
	return out, s.Err()
}

func statPtr(stats map[string]uint64, names ...string) *uint64 {
	for _, name := range names {
		if v, ok := stats[name]; ok {
			copy := v
			return &copy
		}
	}
	return nil
}
