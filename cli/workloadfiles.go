package main

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// fetchWorkloadFiles asks the agent for the workload's mounted secret/configMap
// files (files-of), extracts the tar into a fresh temp directory, and returns
// that directory with the ABSOLUTE mount paths the files came from - what
// localizeFileEnv needs to repoint the variables that name them. An agent that
// predates the verb answers "unknown command"; there are then simply no files,
// not an error. The directory holds copies of secrets, so it is created 0700.
func fetchWorkloadFiles(tr envExecer, name string) (dir string, paths []string, err error) {
	out, err := tr.ExecAll("files-of " + name)
	if err != nil {
		return "", nil, err
	}
	if strings.HasPrefix(out, "error:") {
		if strings.Contains(out, "unknown command") {
			return "", nil, nil // an agent before files-of: no files, not a failure
		}
		return "", nil, fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(out, "error:")))
	}
	var r struct {
		Paths []string `json:"paths"`
		Tar   string   `json:"tar"`
	}
	if json.Unmarshal([]byte(out), &r) != nil || len(r.Paths) == 0 || r.Tar == "" {
		return "", nil, nil // nothing mounted, or an answer we do not recognise
	}
	raw, err := base64.StdEncoding.DecodeString(r.Tar)
	if err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp("", "plug-files-")
	if err != nil {
		return "", nil, err
	}
	if err := untar(raw, dir); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, r.Paths, nil
}

// untar writes a tar's regular files under dir, each member forced to an
// absolute clean path first so nothing (a "../" or an absolute member) can land
// outside dir. Directories are created as needed; symlinks and other types are
// skipped - a secret mount is plain files, and a link is the one member that
// could still point out of the tree.
func untar(raw []byte, dir string) error {
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, filepath.Clean("/"+h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}
