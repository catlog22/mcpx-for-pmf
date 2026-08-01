//go:build !windows

package changeset

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }

var syncDirectory = syncDirectoryImpl

func syncDirectoryImpl(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
