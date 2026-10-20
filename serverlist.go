package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Minimal NBT (uncompressed, big-endian) reader/writer, enough to edit
// servers.dat while keeping every tag Minecraft or mods put there.

const (
	tagEnd byte = iota
	tagByte
	tagShort
	tagInt
	tagLong
	tagFloat
	tagDouble
	tagByteArray
	tagString
	tagList
	tagCompound
	tagIntArray
	tagLongArray
)

type nbtTag struct {
	Type  byte
	Name  string
	Value any // int8, int16, int32, int64, float32, float64, []byte, string, nbtList, nbtCompound, []int32, []int64
}

type nbtList struct {
	Elem  byte
	Items []any
}

type nbtCompound []nbtTag

func (c nbtCompound) get(name string) *nbtTag {
	for i := range c {
		if c[i].Name == name {
			return &c[i]
		}
	}
	return nil
}

func (c nbtCompound) str(name string) string {
	if t := c.get(name); t != nil {
		if s, ok := t.Value.(string); ok {
			return s
		}
	}
	return ""
}

// ---------- read ----------

type nbtReader struct{ r *bufio.Reader }

func (n *nbtReader) read(v any) error { return binary.Read(n.r, binary.BigEndian, v) }

func (n *nbtReader) str() (string, error) {
	var l uint16
	if err := n.read(&l); err != nil {
		return "", err
	}
	b := make([]byte, l)
	_, err := io.ReadFull(n.r, b)
	return string(b), err
}

func (n *nbtReader) length() (int, error) {
	var l int32
	if err := n.read(&l); err != nil {
		return 0, err
	}
	if l < 0 || l > 1<<24 {
		return 0, errors.New("nbt: longueur invalide")
	}
	return int(l), nil
}

func (n *nbtReader) payload(t byte, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("nbt: trop imbriqué")
	}
	switch t {
	case tagByte:
		var v int8
		return v, n.read(&v)
	case tagShort:
		var v int16
		return v, n.read(&v)
	case tagInt:
		var v int32
		return v, n.read(&v)
	case tagLong:
		var v int64
		return v, n.read(&v)
	case tagFloat:
		var v float32
		return v, n.read(&v)
	case tagDouble:
		var v float64
		return v, n.read(&v)
	case tagByteArray:
		l, err := n.length()
		if err != nil {
			return nil, err
		}
		b := make([]byte, l)
		_, err = io.ReadFull(n.r, b)
		return b, err
	case tagString:
		return n.str()
	case tagList:
		var et byte
		if err := n.read(&et); err != nil {
			return nil, err
		}
		l, err := n.length()
		if err != nil {
			return nil, err
		}
		list := nbtList{Elem: et}
		for i := 0; i < l; i++ {
			v, err := n.payload(et, depth+1)
			if err != nil {
				return nil, err
			}
			list.Items = append(list.Items, v)
		}
		return list, nil
	case tagCompound:
		var c nbtCompound
		for {
			var tt byte
			if err := n.read(&tt); err != nil {
				return nil, err
			}
			if tt == tagEnd {
				return c, nil
			}
			name, err := n.str()
			if err != nil {
				return nil, err
			}
			v, err := n.payload(tt, depth+1)
			if err != nil {
				return nil, err
			}
			c = append(c, nbtTag{Type: tt, Name: name, Value: v})
		}
	case tagIntArray:
		l, err := n.length()
		if err != nil {
			return nil, err
		}
		v := make([]int32, l)
		return v, n.read(v)
	case tagLongArray:
		l, err := n.length()
		if err != nil {
			return nil, err
		}
		v := make([]int64, l)
		return v, n.read(v)
	}
	return nil, fmt.Errorf("nbt: type %d inconnu", t)
}

func readNBT(b []byte) (nbtTag, error) {
	n := &nbtReader{r: bufio.NewReader(bytes.NewReader(b))}
	var t byte
	if err := n.read(&t); err != nil {
		return nbtTag{}, err
	}
	if t != tagCompound {
		return nbtTag{}, errors.New("nbt: racine invalide")
	}
	name, err := n.str()
	if err != nil {
		return nbtTag{}, err
	}
	v, err := n.payload(t, 0)
	return nbtTag{Type: t, Name: name, Value: v}, err
}

// ---------- write ----------

func writeStr(w *bytes.Buffer, s string) {
	if len(s) > math.MaxUint16 {
		s = s[:math.MaxUint16]
	}
	_ = binary.Write(w, binary.BigEndian, uint16(len(s)))
	w.WriteString(s)
}

func writePayload(w *bytes.Buffer, t byte, v any) {
	switch t {
	case tagString:
		writeStr(w, v.(string))
	case tagByteArray:
		b := v.([]byte)
		_ = binary.Write(w, binary.BigEndian, int32(len(b)))
		w.Write(b)
	case tagIntArray:
		a := v.([]int32)
		_ = binary.Write(w, binary.BigEndian, int32(len(a)))
		_ = binary.Write(w, binary.BigEndian, a)
	case tagLongArray:
		a := v.([]int64)
		_ = binary.Write(w, binary.BigEndian, int32(len(a)))
		_ = binary.Write(w, binary.BigEndian, a)
	case tagList:
		l := v.(nbtList)
		elem := l.Elem
		if len(l.Items) == 0 && elem == 0 {
			elem = tagEnd
		}
		w.WriteByte(elem)
		_ = binary.Write(w, binary.BigEndian, int32(len(l.Items)))
		for _, it := range l.Items {
			writePayload(w, elem, it)
		}
	case tagCompound:
		for _, c := range v.(nbtCompound) {
			w.WriteByte(c.Type)
			writeStr(w, c.Name)
			writePayload(w, c.Type, c.Value)
		}
		w.WriteByte(tagEnd)
	default: // numbers
		_ = binary.Write(w, binary.BigEndian, v)
	}
}

func writeNBT(root nbtTag) []byte {
	var w bytes.Buffer
	w.WriteByte(root.Type)
	writeStr(&w, root.Name)
	writePayload(&w, root.Type, root.Value)
	return w.Bytes()
}

// ---------- servers.dat ----------

func normServer(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".")
	return strings.TrimSuffix(s, ":25565")
}

// ensureServerListed makes sure the pack's server shows in the in-game
// multiplayer list, so a player who disconnects can join again from there.
// Quick Play saves the server as a *hidden* entry, which is why it vanished.
// The player's other servers and their order are kept.
func ensureServerListed(p *Pack) error {
	addr := strings.TrimSpace(p.Server)
	if addr == "" {
		return nil
	}
	path := filepath.Join(pInstance(p), "servers.dat")
	root := nbtTag{Type: tagCompound, Value: nbtCompound{}}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		r, err := readNBT(b)
		if err != nil {
			return nil // unreadable file: leave it to Minecraft rather than lose the player's list
		}
		root = r
	}
	rc := root.Value.(nbtCompound)
	st := rc.get("servers")
	if st == nil || st.Type != tagList {
		rc = append(rc, nbtTag{Type: tagList, Name: "servers", Value: nbtList{Elem: tagCompound}})
		st = &rc[len(rc)-1]
	}
	list := st.Value.(nbtList)
	if len(list.Items) == 0 {
		list.Elem = tagCompound
	}
	if list.Elem != tagCompound {
		return nil
	}

	want := normServer(addr)
	changed, found := false, false
	var kept []any
	for _, it := range list.Items {
		c := it.(nbtCompound)
		if normServer(c.str("ip")) != want {
			kept = append(kept, c)
			continue
		}
		if found { // duplicate entry of our server: drop it
			changed = true
			continue
		}
		found = true
		if h := c.get("hidden"); h != nil {
			if v, ok := h.Value.(int8); !ok || v != 0 {
				h.Type, h.Value = tagByte, int8(0)
				changed = true
			}
		}
		if c.str("name") == "" || c.str("name") == "Minecraft Server" {
			if t := c.get("name"); t != nil {
				t.Type, t.Value = tagString, p.Name
			} else {
				c = append(c, nbtTag{Type: tagString, Name: "name", Value: p.Name})
			}
			changed = true
		}
		kept = append(kept, c)
	}
	if !found {
		entry := nbtCompound{
			{Type: tagString, Name: "name", Value: p.Name},
			{Type: tagString, Name: "ip", Value: addr},
			{Type: tagByte, Name: "hidden", Value: int8(0)},
		}
		kept = append([]any{entry}, kept...)
		changed = true
	}
	if !changed {
		return nil
	}
	list.Items = kept
	st.Value = list
	root.Value = rc
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, writeNBT(root), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
