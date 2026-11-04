package filepolicy

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicrepository/nchat/libs/go/platform/documentguard"
)

// ZIP's ReaderAt adapter is deliberately sequential. No caller may share this
// cursor; archive/zip reads entries one at a time here.
type seekReaderAt struct{ source io.ReadSeeker }

func (r seekReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if _, err := r.source.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(r.source, p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return n, err
}

type zipInspection struct {
	reader    io.ReaderAt
	files     map[string]*zip.File
	order     []*zip.File
	directory int64
}

func inspectZIP(ctx context.Context, r io.ReadSeeker, size int64, f Format, p Profiles) error {
	if size > MaxContainerBytes {
		return errBudget
	}
	z, err := openZIP(r, size)
	if err != nil {
		return err
	}
	if err = z.checkLayout(); err != nil {
		return err
	}
	switch f.Strategy {
	case OOXML:
		err = z.validateOOXML(ctx, f)
	case ODF:
		err = z.validateODF(ctx, f)
	}
	if err != nil {
		return err
	}
	return z.inspectEntries(ctx, f, p)
}

func openZIP(r io.ReadSeeker, size int64) (*zipInspection, error) {
	if size < 22 {
		return nil, errStructure
	}
	ra := seekReaderAt{r}
	var end [22]byte
	if _, err := ra.ReadAt(end[:], size-22); err != nil {
		return nil, err
	}
	if !bytes.Equal(end[:4], []byte{'P', 'K', 5, 6}) {
		return nil, errStructure
	}
	count := binary.LittleEndian.Uint16(end[10:12])
	if count > MaxEntries {
		return nil, errBudget
	}
	if !validZIPEnd(end[:], size) {
		return nil, errStructure
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, errStructure
	}
	if len(zr.File) != int(count) || count == 0 {
		return nil, errStructure
	}
	z := &zipInspection{reader: ra, files: make(map[string]*zip.File), order: zr.File, directory: int64(binary.LittleEndian.Uint32(end[16:20]))}
	if err = z.index(); err != nil {
		return nil, err
	}
	return z, nil
}

func validZIPEnd(b []byte, size int64) bool {
	// No split disks, ZIP64 or archive comments. With the 20 MiB cap, none
	// is needed; refusing alternate directory interpretations is intentional.
	return binary.LittleEndian.Uint32(b[4:8]) == 0 &&
		binary.LittleEndian.Uint32(b[12:16]) <= MaxEntries*(46+MaxNameBytes+1024+MaxNameBytes) &&
		binary.LittleEndian.Uint16(b[8:10]) == binary.LittleEndian.Uint16(b[10:12]) &&
		binary.LittleEndian.Uint16(b[20:22]) == 0 &&
		int64(binary.LittleEndian.Uint32(b[12:16]))+int64(binary.LittleEndian.Uint32(b[16:20])) == size-22
}

func (z *zipInspection) index() error {
	var expanded uint64
	for _, f := range z.order {
		if !safeEntryName(f.Name) {
			return errStructure
		}
		name := strings.ToLower(f.Name)
		if z.files[name] != nil {
			return errStructure
		}
		if err := entryMetadata(f); err != nil {
			return err
		}
		expanded += f.UncompressedSize64
		if expanded > MaxExpandedBytes {
			return errBudget
		}
		z.files[name] = f
	}
	return nil
}

func safeEntryName(name string) bool {
	if len(name) > MaxNameBytes || !utf8.ValidString(name) {
		return false
	}
	if invalidEntryPrefix(name) {
		return false
	}
	trim := strings.TrimSuffix(name, "/")
	if !fs.ValidPath(trim) || path.Clean(trim) != trim {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r) {
			return false
		}
	}
	return true
}

func entryMetadata(f *zip.File) error {
	if unsupportedZIPEncoding(f) {
		return errOpaque
	}
	if f.Mode().Type() != 0 && !f.Mode().IsDir() {
		return errActive
	}
	if err := zipEntryBudget(f); err != nil {
		return err
	}
	if f.FileInfo().IsDir() {
		return emptyZIPDirectory(f)
	}
	return validateZIPExtra(f.Extra)
}

func emptyZIPDirectory(f *zip.File) error {
	if f.CompressedSize64 != 0 || f.UncompressedSize64 != 0 || f.CRC32 != 0 {
		return errStructure
	}
	return validateZIPExtra(f.Extra)
}

func validateZIPExtra(extra []byte) error {
	for len(extra) > 0 {
		if len(extra) < 4 {
			return errStructure
		}
		kind := binary.LittleEndian.Uint16(extra[:2])
		size := int(binary.LittleEndian.Uint16(extra[2:4]))
		if size > len(extra)-4 {
			return errStructure
		}
		// Timestamp, Unix UID/GID and NTFS timestamps carry no alternate name,
		// encryption or ZIP64 interpretation. Every other extension is opaque.
		if kind != 0x5455 && kind != 0x7875 && kind != 0x000a {
			return errOpaque
		}
		extra = extra[4+size:]
	}
	return nil
}

func (z *zipInspection) checkLayout() error {
	sort.Slice(z.order, func(i, j int) bool { a, _ := z.order[i].DataOffset(); b, _ := z.order[j].DataOffset(); return a < b })
	var next int64
	for _, f := range z.order {
		offset, err := f.DataOffset()
		if err != nil {
			return errStructure
		}
		end, err := z.localEntry(f, next, offset)
		if err != nil {
			return err
		}
		next = end
	}
	if next != z.directory {
		return errStructure
	}
	return nil
}

func (z *zipInspection) localEntry(f *zip.File, start, offset int64) (int64, error) {
	var h [30]byte
	if _, err := z.reader.ReadAt(h[:], start); err != nil {
		return 0, err
	}
	if !bytes.Equal(h[:4], []byte{'P', 'K', 3, 4}) {
		return 0, errStructure
	}
	nameLen := int64(binary.LittleEndian.Uint16(h[26:28]))
	extraLen := int64(binary.LittleEndian.Uint16(h[28:30]))
	if !matchingLocalGeometry(f.Name, start, offset, nameLen, extraLen) {
		return 0, errStructure
	}
	name := make([]byte, nameLen)
	if _, err := z.reader.ReadAt(name, start+30); err != nil {
		return 0, err
	}
	if !matchingLocalHeader(f, h[:], name) {
		return 0, errStructure
	}
	if err := z.localExtra(start+30+nameLen, extraLen); err != nil {
		return 0, err
	}
	end, err := compressedEnd(offset, f.CompressedSize64, z.directory)
	if err != nil {
		return 0, err
	}
	if f.Flags&8 != 0 {
		return z.descriptor(f, end)
	}
	if !matchingLocalSizes(f, h[:]) {
		return 0, errStructure
	}
	return end, nil
}

func (z *zipInspection) localExtra(offset, size int64) error {
	extra := make([]byte, size)
	if _, err := z.reader.ReadAt(extra, offset); err != nil {
		return err
	}
	return validateZIPExtra(extra)
}

func (z *zipInspection) descriptor(f *zip.File, start int64) (int64, error) {
	var b [16]byte
	if _, err := z.reader.ReadAt(b[:], start); err != nil {
		return 0, err
	}
	if !bytes.Equal(b[:4], []byte{'P', 'K', 7, 8}) {
		return 0, errStructure
	}
	if binary.LittleEndian.Uint32(b[4:8]) != f.CRC32 || uint64(binary.LittleEndian.Uint32(b[8:12])) != f.CompressedSize64 || uint64(binary.LittleEndian.Uint32(b[12:16])) != f.UncompressedSize64 {
		return 0, errStructure
	}
	return start + 16, nil
}

func (z *zipInspection) inspectEntries(ctx context.Context, format Format, p Profiles) error {
	for _, f := range z.order {
		if f.FileInfo().IsDir() {
			continue
		}
		if err := z.inspectEntry(ctx, f, format, p); err != nil {
			return err
		}
	}
	return nil
}

func (z *zipInspection) readEntry(ctx context.Context, f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > uint64(MaxTextBytes) {
		return nil, errBudget
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, errStructure
	}
	compressed := &zipCompressedReader{source: contextReader{ctx, raw}}
	reader, closeReader := zipDecoder(f.Method, compressed)
	defer closeReader()
	b, err := io.ReadAll(io.LimitReader(reader, MaxTextBytes+1))
	if err != nil {
		return nil, errStructure
	}
	if int64(len(b)) > MaxTextBytes {
		return nil, errBudget
	}
	if uint64(len(b)) != f.UncompressedSize64 || compressed.count != f.CompressedSize64 || crc32.ChecksumIEEE(b) != f.CRC32 {
		return nil, errStructure
	}
	return b, nil
}

// ReadByte prevents flate from reading beyond its end marker. Counting raw
// bytes then detects hidden trailing data inside the compressed entry.
type zipCompressedReader struct {
	source io.Reader
	count  uint64
}

func (r *zipCompressedReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n > 0 {
		r.count += uint64(n)
	}
	return n, err
}
func (r *zipCompressedReader) ReadByte() (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(r, b[:])
	return b[0], err
}
func zipDecoder(method uint16, r *zipCompressedReader) (io.Reader, func()) {
	if method == zip.Deflate {
		decoder := flate.NewReader(r)
		return decoder, func() { _ = decoder.Close() }
	}
	return r, func() {}
}

func (z *zipInspection) inspectEntry(ctx context.Context, f *zip.File, format Format, p Profiles) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := z.readEntry(ctx, f)
	if err != nil {
		return err
	}
	if bytes.HasPrefix(b, []byte("PK")) || dangerousHead(b) {
		return errActive
	}
	if format.Strategy == OOXML || format.Strategy == ODF {
		return inspectOfficePart(ctx, f.Name, b, z.hasPart)
	}
	return inspectArchiveLeaf(ctx, f.Name, b, p)
}

func inspectArchiveLeaf(ctx context.Context, name string, b []byte, p Profiles) error {
	entryFormat, ok := byName(name)
	if !ok || containerStrategy(entryFormat.Strategy) {
		return errOpaque
	}
	result, err := Inspect(ctx, bytes.NewReader(b), int64(len(b)), name, "", p)
	if err != nil {
		return err
	}
	if result.Decision != Allow {
		return errOpaque
	}
	return nil
}

func inspectOfficePart(ctx context.Context, name string, b []byte, exists func(string) bool) error {
	lower := strings.ToLower(name)
	if activeOfficePath(lower) {
		return errActive
	}
	if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".rels") {
		return inspectOfficeXML(ctx, name, b, exists)
	}
	if lower == "mimetype" {
		return nil
	}
	f, ok := byName(name)
	if !ok || f.Family != Image {
		return errOpaque
	}
	return inspectSignature(ctx, bytes.NewReader(b), int64(len(b)), b[:min(512, len(b))], f)
}

func activeOfficePath(name string) bool {
	for _, part := range []string{"/embeddings/", "/activex/", "/externallinks/", "/vbaproject", "/scripts/", "/basic/", "/connections", "/querytables/"} {
		if strings.Contains("/"+name, part) {
			return true
		}
	}
	return strings.HasPrefix(name, "object ")
}

func invalidEntryPrefix(name string) bool {
	return strings.ContainsAny(name, "\\:%") || strings.HasPrefix(name, "/")
}

func matchingLocalHeader(f *zip.File, h, name []byte) bool {
	return string(name) == f.Name && binary.LittleEndian.Uint16(h[6:8]) == f.Flags && binary.LittleEndian.Uint16(h[8:10]) == f.Method
}

func matchingLocalSizes(f *zip.File, h []byte) bool {
	return binary.LittleEndian.Uint32(h[14:18]) == f.CRC32 && uint64(binary.LittleEndian.Uint32(h[18:22])) == f.CompressedSize64 && uint64(binary.LittleEndian.Uint32(h[22:26])) == f.UncompressedSize64
}

func containerStrategy(s Strategy) bool { return s == ZIP || s == OOXML || s == ODF }

func unsupportedZIPEncoding(f *zip.File) bool {
	return f.Flags & ^uint16(0x808) != 0 || (f.Method != zip.Store && f.Method != zip.Deflate)
}

func matchingLocalGeometry(name string, start, offset, nameLen, extraLen int64) bool {
	return nameLen == int64(len(name)) && extraLen <= 1024 && offset == start+30+nameLen+extraLen
}

func zipEntryBudget(f *zip.File) error {
	if f.UncompressedSize64 > MaxExpandedBytes {
		return errBudget
	}
	if f.CompressedSize64 > uint64(MaxContainerBytes) {
		return errBudget
	}
	if f.UncompressedSize64 > max(1, f.CompressedSize64)*MaxExpansionRatio {
		return errBudget
	}
	if len(f.Extra) > 1024 || len(f.Comment) > MaxNameBytes {
		return errBudget
	}
	return nil
}

func compressedEnd(offset int64, size uint64, directory int64) (int64, error) {
	if size > uint64(MaxContainerBytes) {
		return 0, errBudget
	}
	end := offset + int64(size)
	if end > directory {
		return 0, errStructure
	}
	return end, nil
}

func inspectOfficeXML(ctx context.Context, name string, b []byte, exists func(string) bool) error {
	if !passiveText(b) {
		return errActive
	}
	if err := officeXMLGrammar(ctx, name, b); err != nil {
		return err
	}
	if err := documentguard.Inspect(ctx, b, name, exists); err != nil {
		return errActive
	}
	return nil
}

func officeXMLGrammar(ctx context.Context, name string, b []byte) error {
	if strings.HasPrefix(strings.ToLower(name), "xl/worksheets/") {
		return validateWorksheet(ctx, b)
	}
	return validateXML(ctx, b, nil)
}
