package documentguard

import (
	"archive/zip"
	"context"
	"io"
	"path"
	"strings"
)

const (
	MaxEntries       = 2048
	MaxExpandedBytes = 32 << 20
	MaxExpansionRate = 100
)

// Archive is a bounded index of exact package names, not extracted content.
type Archive struct {
	names map[string]bool
	MIME  string
}

func (a Archive) Has(name string) bool { return a.names[name] }

// ArchiveXML checks every XML/relationship part before any converter is invoked.
func ArchiveXML(ctx context.Context, z *zip.Reader) (Archive, error) {
	index := Archive{names: map[string]bool{}}
	if len(z.File) == 0 || len(z.File) > MaxEntries {
		return index, ErrUnsafe
	}
	var expanded uint64
	folded := map[string]bool{}
	for _, f := range z.File {
		if err := archiveEntry(f, &expanded); err != nil {
			return index, err
		}
		lower := strings.ToLower(f.Name)
		if folded[lower] {
			return index, ErrUnsafe
		}
		folded[lower] = true
		index.names[f.Name] = true
	}
	for _, f := range z.File {
		if err := index.inspect(ctx, f); err != nil {
			return index, err
		}
	}
	return index, nil
}

func archiveEntry(f *zip.File, total *uint64) error {
	if err := entryPath(f); err != nil {
		return err
	}
	if f.UncompressedSize64 > MaxXMLBytes {
		return ErrUnsafe
	}
	*total += f.UncompressedSize64
	if *total > MaxExpandedBytes || f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > MaxExpansionRate {
		return ErrUnsafe
	}
	return passivePath(strings.ToLower(f.Name))
}

func passivePath(name string) error {
	for _, part := range []string{"vbaproject", "/activex/", "/embeddings/", "/externallinks/", "/object ", "/scripts/", "/basic/"} {
		if strings.Contains("/"+name, part) {
			return ErrUnsafe
		}
	}
	return nil
}

func (a *Archive) inspect(ctx context.Context, f *zip.File) error {
	lower := strings.ToLower(f.Name)
	if lower != "mimetype" && !strings.HasSuffix(lower, ".xml") && !RelationshipPart(lower) {
		return nil
	}
	body, err := readEntry(f)
	if err != nil {
		return err
	}
	if lower == "mimetype" {
		if len(body) > 256 {
			return ErrUnsafe
		}
		a.MIME = strings.TrimSpace(string(body))
		return nil
	}
	return Inspect(ctx, body, f.Name, a.Has)
}

func readEntry(f *zip.File) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, ErrUnsafe
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(io.LimitReader(r, MaxXMLBytes+1))
	if err != nil || len(b) > MaxXMLBytes {
		return nil, ErrUnsafe
	}
	return b, nil
}

func entryPath(f *zip.File) error {
	name := f.Name
	if strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") {
		return ErrUnsafe
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.TrimSuffix(name, "/") != clean {
		return ErrUnsafe
	}
	if !f.Mode().IsRegular() && !f.FileInfo().IsDir() {
		return ErrUnsafe
	}
	return nil
}
