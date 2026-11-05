package feishu

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
)

// eventLedger 以独占创建的标记保留入站事件的去重凭据。
type eventLedger struct {
	root *os.Root
}

func openEventLedger(dir string) (*eventLedger, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &eventLedger{root: root}, nil
}

func (l *eventLedger) claim(eventKey string) (bool, error) {
	name := fmt.Sprintf("%x.received", sha256.Sum256([]byte(eventKey)))
	f, err := l.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return false, err
	}
	dir, err := l.root.Open(".")
	if err != nil {
		return false, err
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return false, err
	}
	return true, nil
}
