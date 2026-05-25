// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"golang.org/x/arch/arm64/arm64asm"
	"golang.org/x/arch/x86/x86asm"
)

type Symbol struct {
	s       *elf.Symbol
	f       *ELFFile
	address uint64
}

func (s *Symbol) Name() string {
	return s.s.Name
}

func (s *Symbol) Address() uint64 {
	if s.address == 0 {
		s.address = s.s.Value
		for _, p := range s.f.elf.Progs {
			if p.Type != elf.PT_LOAD || (p.Flags&elf.PF_X) == 0 {
				continue
			}
			if p.Vaddr <= s.s.Value && s.s.Value < (p.Vaddr+p.Memsz) {
				s.address = s.s.Value - p.Vaddr + p.Off
				break
			}
		}
	}
	return s.address
}

func (s *Symbol) ReturnOffsets() ([]int, error) {
	text, reader, err := s.f.getTextSectionAndReader()
	if err != nil {
		return nil, err
	}

	sStart := s.s.Value - text.Addr
	_, err = reader.Seek(int64(sStart), io.SeekStart)
	if err != nil {
		return nil, err
	}
	sBytes := make([]byte, s.s.Size)
	_, err = reader.Read(sBytes)
	if err != nil {
		return nil, err
	}

	offsets := getReturnOffsets(s.f.elf.Machine, sBytes)
	if len(offsets) == 0 {
		return nil, fmt.Errorf("no offsets found")
	}
	return offsets, nil
}

func (s *Symbol) AttachUprobe(exe *link.Executable, prog *ebpf.Program, pid uint32) (link.Link, error) {
	return exe.Uprobe(s.Name(), prog, &link.UprobeOptions{Address: s.Address(), PID: int(pid)})
}

func (s *Symbol) AttachUretprobes(exe *link.Executable, prog *ebpf.Program, pid uint32) ([]link.Link, error) {
	returnOffsets, err := s.ReturnOffsets()
	if err != nil {
		return nil, err
	}
	var links []link.Link
	for _, offset := range returnOffsets {
		l, err := exe.Uprobe("pthread_cond_timedwait", prog, &link.UprobeOptions{Address: s.Address(), Offset: uint64(offset), PID: int(pid)})
		if err != nil {
			return links, err
		}
		links = append(links, l)
	}

	return links, nil
}

type ELFFile struct {
	elf               *elf.File
	symbols           []elf.Symbol
	textSection       *elf.Section
	textSectionReader io.ReadSeeker
}

func OpenELFFile(path string) (*ELFFile, error) {
	file, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	return &ELFFile{elf: file}, nil
}

// readSymbols loads the symbol tables and keeps ONLY entries whose name
// appears in `wanted`. Callers that need multiple symbols should pass them all
// at once so the full ~100 MB symbol slice from debug/elf can be GC'd in the
// same scope, rather than retained on the ELFFile across lookups.
func (f *ELFFile) readSymbols(wanted map[string]struct{}) error {
	keep := func(src []elf.Symbol) {
		for i := range src {
			s := &src[i]
			if elf.ST_TYPE(s.Info) != elf.STT_FUNC || s.Size == 0 || s.Value == 0 {
				continue
			}
			if _, ok := wanted[s.Name]; !ok {
				continue
			}
			if s.VersionIndex&0x8000 != 0 {
				continue
			}
			f.symbols = append(f.symbols, *s)
		}
		// drop reference to the full slice so GC can reclaim it now
		for i := range src {
			src[i] = elf.Symbol{}
		}
	}
	if syms, err := f.elf.Symbols(); err == nil {
		keep(syms)
	}
	if dyn, err := f.elf.DynamicSymbols(); err == nil {
		keep(dyn)
	}
	if len(f.symbols) == 0 {
		return fmt.Errorf("no symbols found")
	}
	return nil
}

func (f *ELFFile) GetSymbol(name string) (*Symbol, error) {
	if f.symbols == nil {
		if err := f.readSymbols(map[string]struct{}{name: {}}); err != nil {
			return nil, err
		}
	}
	var es *elf.Symbol
	for i := range f.symbols {
		if f.symbols[i].Name == name {
			es = &f.symbols[i]
			break
		}
	}
	if es == nil {
		return nil, fmt.Errorf("symbol %s not found", name)
	}
	return &Symbol{s: es, f: f}, nil
}

// GetSymbols is the multi-name variant — call this when you need several
// symbols from one binary so the full debug/elf symbol slice is allocated and
// released exactly once.
func (f *ELFFile) GetSymbols(names ...string) (map[string]*Symbol, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, n := range names {
		wanted[n] = struct{}{}
	}
	if err := f.readSymbols(wanted); err != nil {
		return nil, err
	}
	out := make(map[string]*Symbol, len(names))
	for i := range f.symbols {
		s := &f.symbols[i]
		if _, ok := wanted[s.Name]; ok {
			out[s.Name] = &Symbol{s: s, f: f}
		}
	}
	return out, nil
}

// findSymbolsStreaming scans the ELF symbol table (.symtab then .dynsym) for
// the wanted names without ever materialising the full []elf.Symbol slice
// that debug/elf.Symbols()/DynamicSymbols() would allocate (~50-70 MB
// transient for a typical Go binary). It reads Elf64_Sym entries 24 bytes at
// a time via the symbol section's io.ReadSeeker, and resolves each candidate
// name individually from the linked string-table section's ReaderAt.
//
// Returns a map name → elf.Symbol containing only the matched entries
// (Name, Info, Value, Size and Section populated — enough for callers to
// resolve file-offset addresses and to extract bytes from .text for
// uretprobe RET-offset discovery).
//
// ELF32 binaries are explicitly rejected — the agent only supports ELF64
// targets on Linux/amd64/arm64.
func (f *ELFFile) findSymbolsStreaming(wanted map[string]struct{}) (map[string]elf.Symbol, error) {
	if len(wanted) == 0 {
		return map[string]elf.Symbol{}, nil
	}
	if f.elf.Class != elf.ELFCLASS64 {
		return nil, fmt.Errorf("findSymbolsStreaming: unsupported ELF class %v (only ELF64 supported)", f.elf.Class)
	}

	out := make(map[string]elf.Symbol, len(wanted))
	remaining := make(map[string]struct{}, len(wanted))
	for n := range wanted {
		remaining[n] = struct{}{}
	}

	// Pre-compute the max wanted-name length so we can size the strtab read
	// buffer to (longest+1) and detect both "shorter than target" (partial
	// match — skip) and "longer than target" (must have NUL after target —
	// skip if a different character follows).
	maxLen := 0
	for n := range wanted {
		if len(n) > maxLen {
			maxLen = len(n)
		}
	}
	nameBuf := make([]byte, maxLen+1)

	scan := func(sectionName string) error {
		if len(remaining) == 0 {
			return nil
		}
		symSec := f.elf.Section(sectionName)
		if symSec == nil {
			return fmt.Errorf("no %s section", sectionName)
		}
		if int(symSec.Link) >= len(f.elf.Sections) {
			return fmt.Errorf("invalid Link index for %s", sectionName)
		}
		strtab := f.elf.Sections[symSec.Link]
		if strtab == nil {
			return fmt.Errorf("no string table for %s", sectionName)
		}

		const entrySize = 24 // Elf64_Sym
		symReader := symSec.Open()
		entry := make([]byte, entrySize)

		// Skip entry 0 (the always-undefined STN_UNDEF symbol).
		if _, err := io.ReadFull(symReader, entry); err != nil {
			return fmt.Errorf("read %s header: %w", sectionName, err)
		}

		for {
			if _, err := io.ReadFull(symReader, entry); err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					break
				}
				return fmt.Errorf("read %s entry: %w", sectionName, err)
			}

			// Elf64_Sym layout:
			//   st_name  u32 @ 0
			//   st_info  u8  @ 4
			//   st_other u8  @ 5
			//   st_shndx u16 @ 6
			//   st_value u64 @ 8
			//   st_size  u64 @ 16
			nameIdx := binary.LittleEndian.Uint32(entry[0:4])
			stInfo := entry[4]
			stValue := binary.LittleEndian.Uint64(entry[8:16])
			stSize := binary.LittleEndian.Uint64(entry[16:24])

			// Early-filter mirrors readSymbols(): only STT_FUNC entries
			// with non-zero size and value can ever be uprobe targets.
			if elf.ST_TYPE(stInfo) != elf.STT_FUNC || stSize == 0 || stValue == 0 {
				continue
			}
			if nameIdx == 0 {
				continue
			}

			n, err := strtab.ReadAt(nameBuf, int64(nameIdx))
			if err != nil && err != io.EOF {
				continue
			}
			if n == 0 {
				continue
			}
			// Find NUL within nameBuf — name length is bounded by maxLen.
			nameLen := bytes.IndexByte(nameBuf[:n], 0)
			if nameLen < 0 {
				// Name is longer than our max wanted name → cannot match.
				continue
			}
			if _, ok := remaining[string(nameBuf[:nameLen])]; !ok {
				continue
			}
			name := string(nameBuf[:nameLen])
			out[name] = elf.Symbol{
				Name:    name,
				Info:    stInfo,
				Other:   entry[5],
				Section: elf.SectionIndex(binary.LittleEndian.Uint16(entry[6:8])),
				Value:   stValue,
				Size:    stSize,
			}
			delete(remaining, name)
			if len(remaining) == 0 {
				return nil
			}
		}
		return nil
	}

	// .symtab first (richer; present in unstripped binaries), then .dynsym
	// for whatever wasn't found there. Errors from one section are non-fatal
	// as long as the other yields something.
	errSym := scan(".symtab")
	errDyn := scan(".dynsym")
	if len(out) == 0 {
		if errSym != nil && errDyn != nil {
			return nil, fmt.Errorf("no symbols found (symtab: %v; dynsym: %v)", errSym, errDyn)
		}
		return nil, fmt.Errorf("no matching symbols found")
	}
	return out, nil
}

func (f *ELFFile) getTextSectionAndReader() (*elf.Section, io.ReadSeeker, error) {
	if f.textSection == nil {
		f.textSection = f.elf.Section(".text")
		if f.textSection == nil {
			return nil, nil, fmt.Errorf("no .text")
		}
		f.textSectionReader = f.textSection.Open()
	}
	return f.textSection, f.textSectionReader, nil
}

func (f *ELFFile) Close() error {
	// Drop the parsed symbol table so GC can reclaim it immediately, instead
	// of waiting until the *ELFFile pointer itself becomes unreachable. For
	// large Go binaries this is ~100 MB of strings + Symbol structs per call.
	f.symbols = nil
	f.textSection = nil
	f.textSectionReader = nil
	return f.elf.Close()
}

func getReturnOffsets(machine elf.Machine, instructions []byte) []int {
	var res []int
	switch machine {
	case elf.EM_X86_64:
		for i := 0; i < len(instructions); {
			ins, err := x86asm.Decode(instructions[i:], 64)
			if err == nil && ins.Op == x86asm.RET {
				res = append(res, i)
			}
			i += ins.Len
		}
	case elf.EM_AARCH64:
		for i := 0; i < len(instructions); {
			ins, err := arm64asm.Decode(instructions[i:])
			if err == nil && ins.Op == arm64asm.RET {
				res = append(res, i)
			}
			i += 4
		}
	}
	return res
}
