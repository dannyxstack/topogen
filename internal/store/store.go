// Package store 把模拟的元数据和快照保存在本地目录：
//
//	<dir>/sims/<id>/meta.json     元数据(所有者、创建参数、进度)
//	<dir>/sims/<id>/snapshot.gz   engine.Sim 快照，地形生成完成后才有
//
// 写入都先写临时文件再改名，进程中途退出不会留下半个文件。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var ErrNotFound = errors.New("not found")

var validID = regexp.MustCompile(`^[0-9a-f]{12}$`)

type Meta struct {
	ID         string          `json:"id"`
	Owner      string          `json:"owner"`
	Created    time.Time       `json:"created"`
	Updated    time.Time       `json:"updated"`
	Request    json.RawMessage `json:"request"` // 创建时的 API 参数，可原样复现
	State      string          `json:"state"`
	Tick       int64           `json:"tick"`
	TotalTicks int64           `json:"totalTicks"`
	Width      int             `json:"width"`
	Length     int             `json:"length"`
}

type Store struct {
	dir string
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sims"), 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (st *Store) simDir(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid id %q", id)
	}
	return filepath.Join(st.dir, "sims", id), nil
}

func writeAtomic(path string, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (st *Store) SaveMeta(m Meta) error {
	dir, err := st.simDir(m.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m.Updated = time.Now()
	return writeAtomic(filepath.Join(dir, "meta.json"), func(w io.Writer) error {
		return json.NewEncoder(w).Encode(m)
	})
}

func (st *Store) SaveSnapshot(id string, write func(io.Writer) error) error {
	dir, err := st.simDir(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "snapshot.gz"), write)
}

func (st *Store) LoadMeta(id string) (Meta, error) {
	var m Meta
	dir, err := st.simDir(id)
	if err != nil {
		return m, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, ErrNotFound
	} else if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// OpenSnapshot 返回快照内容；还没有快照时返回 ErrNotFound
func (st *Store) OpenSnapshot(id string) (io.ReadCloser, error) {
	dir, err := st.simDir(id)
	if err != nil {
		return nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(dir, "snapshot.gz"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

func (st *Store) Delete(id string) error {
	dir, err := st.simDir(id)
	if err != nil {
		return ErrNotFound
	}
	return os.RemoveAll(dir)
}

// List 返回某个所有者的模拟，最近更新的在前；owner 为空时返回全部
func (st *Store) List(owner string) ([]Meta, error) {
	entries, err := os.ReadDir(filepath.Join(st.dir, "sims"))
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() || !validID.MatchString(e.Name()) {
			continue
		}
		m, err := st.LoadMeta(e.Name())
		if err != nil || (owner != "" && m.Owner != owner) {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}
