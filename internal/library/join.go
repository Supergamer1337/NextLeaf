package library

import "bytes"

// KeyIndex keys books so that several descriptions of one book share a key.
// Sources describe a book differently — a longer title, an edition's
// illustrator among the authors — so the plain BookKey alone splits it. The
// index joins plain keys on a shared ISBN, the one piece of certain evidence
// sources have in common. It never guesses from a likeness: an adaptation
// crediting the original author looks exactly like a second copy.
type KeyIndex struct {
	parent map[string]string
	canon  map[string]string // cluster root -> the cluster's key
}

// NewKeyIndex indexes every entry of the given lists.
func NewKeyIndex(lists ...[]Entry) *KeyIndex {
	ix := &KeyIndex{parent: map[string]string{}, canon: map[string]string{}}

	byISBN := map[string]string{} // ISBN -> the first plain key seen carrying it
	for _, list := range lists {
		for _, e := range list {
			k := dedupKey(e)
			if k == "" {
				continue
			}
			for _, raw := range e.Book.ISBNs {
				isbn := normalizeISBN(raw)
				if isbn == "" {
					continue
				}
				if first, ok := byISBN[isbn]; ok {
					ix.union(first, k)
				} else {
					byISBN[isbn] = k
				}
			}
		}
	}

	// A cluster is named by its smallest member, so the name does not depend
	// on which source answered first.
	for k := range ix.parent {
		root := ix.find(k)
		if c, ok := ix.canon[root]; !ok || k < c {
			ix.canon[root] = k
		}
	}
	return ix
}

// Key returns the entry's book key: shared with every other description of
// the same book the index knows. Empty still means "never merge".
func (ix *KeyIndex) Key(e Entry) string {
	return ix.Canonical(dedupKey(e))
}

// Canonical resolves a plain book key to its book's shared key. Statements
// are anchored to plain keys, and this is how they follow a book into a join.
func (ix *KeyIndex) Canonical(plain string) string {
	if c, ok := ix.canon[ix.find(plain)]; ok {
		return c
	}
	return plain
}

func (ix *KeyIndex) find(k string) string {
	p, ok := ix.parent[k]
	if !ok || p == k {
		return k
	}
	root := ix.find(p)
	ix.parent[k] = root
	return root
}

func (ix *KeyIndex) union(a, b string) {
	if a == b {
		return
	}
	ra, rb := ix.find(a), ix.find(b)
	// Every joined key gets an entry, so naming the clusters can walk them.
	ix.parent[ra] = rb
	ix.parent[rb] = rb
}

// normalizeISBN reduces an ISBN to its 13-digit form so the two notations of
// one number compare equal. Anything that is not an ISBN yields "". It runs
// once per ISBN per render, over thousands of them, so it does not allocate
// until it has an answer.
func normalizeISBN(s string) string {
	var d [13]byte
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 'x' {
			c = 'X'
		}
		if (c < '0' || c > '9') && c != 'X' {
			continue
		}
		if n == len(d) {
			return ""
		}
		d[n] = c
		n++
	}
	switch {
	case n == 13 && bytes.IndexByte(d[:], 'X') < 0:
		return string(d[:])
	case n == 10 && bytes.IndexByte(d[:9], 'X') < 0:
		isbn := [13]byte{'9', '7', '8'}
		copy(isbn[3:], d[:9])
		sum := 0
		for i, r := range isbn[:12] {
			w := 1
			if i%2 == 1 {
				w = 3
			}
			sum += int(r-'0') * w
		}
		isbn[12] = byte('0' + (10-sum%10)%10)
		return string(isbn[:])
	}
	return ""
}
