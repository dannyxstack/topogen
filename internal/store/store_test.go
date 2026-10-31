package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := Meta{ID: "aaaaaaaaaaaa", Owner: "u1", Created: time.Now(), Request: json.RawMessage(`{"width":64}`), State: "paused", Tick: 5}
	b := Meta{ID: "bbbbbbbbbbbb", Owner: "u2", Created: time.Now()}
	if err := st.SaveMeta(a); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := st.SaveMeta(b); err != nil {
		t.Fatal(err)
	}

	got, err := st.LoadMeta(a.ID)
	if err != nil || got.Tick != 5 || string(got.Request) != `{"width":64}` {
		t.Fatalf("LoadMeta = %+v, %v", got, err)
	}
	if _, err := st.OpenSnapshot(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot before save: %v", err)
	}
	if err := st.SaveSnapshot(a.ID, func(w io.Writer) error { _, err := w.Write([]byte("snap")); return err }); err != nil {
		t.Fatal(err)
	}
	rc, err := st.OpenSnapshot(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(data, []byte("snap")) {
		t.Fatalf("snapshot = %q", data)
	}

	mine, _ := st.List("u1")
	all, _ := st.List("")
	if len(mine) != 1 || mine[0].ID != a.ID || len(all) != 2 || all[0].ID != b.ID {
		t.Fatalf("List: mine=%v all=%v", mine, all)
	}

	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadMeta(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestStoreRejectsBadIDs(t *testing.T) {
	st, _ := Open(t.TempDir())
	for _, id := range []string{"../etc", "ABCDEFABCDEF", "", "aaaaaaaaaaa/"} {
		if err := st.SaveMeta(Meta{ID: id}); err == nil {
			t.Fatalf("id %q accepted", id)
		}
		if _, err := st.LoadMeta(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("load %q: %v", id, err)
		}
	}
}
