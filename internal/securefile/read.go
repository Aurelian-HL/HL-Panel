package securefile

import (
	"errors"
	"io"
	"os"
)

// ReadBoundedRegular rejects path replacement between inspection and open and
// bounds the bytes read even if the file grows after inspection.
func ReadBoundedRegular(path string, maximum int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > maximum {
		return nil, errors.New("secret must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || after.Size() > maximum || !os.SameFile(before, after) {
		return nil, errors.New("secret file changed or exceeds size limit")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("secret exceeds size limit")
	}
	return raw, nil
}
