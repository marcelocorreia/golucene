package lucene41

import (
	"fmt"
	"github.com/balzaczyy/golucene/core/codec"
	. "github.com/balzaczyy/golucene/core/codec/spi"
	. "github.com/balzaczyy/golucene/core/index/model"
	. "github.com/balzaczyy/golucene/core/search/model"
	"github.com/balzaczyy/golucene/core/store"
	"github.com/balzaczyy/golucene/core/util"
	"log"
)

// Lucene41PostingsReader.java

const (
	LUCENE41_DOC_EXTENSION = "doc"
	LUCENE41_POS_EXTENSION = "pos"
	LUCENE41_PAY_EXTENSION = "pay"

	LUCENE41_BLOCK_SIZE = 128

	LUCENE41_TERMS_CODEC = "Lucene41PostingsWriterTerms"
	LUCENE41_DOC_CODEC   = "Lucene41PostingsWriterDoc"
	LUCENE41_POS_CODEC   = "Lucene41PostingsWriterPos"
	LUCENE41_PAY_CODEC   = "Lucene41PostingsWriterPay"

	LUCENE41_VERSION_START         = 0
	LUCENE41_VERSION_META_ARRAY    = 1
	LUCENE41_VERSION_META_CHECKSUM = 2
	LUCENE41_VERSION_CURRENT       = LUCENE41_VERSION_META_CHECKSUM
)

/*
Concrete class that reads docId (maybe frq,pos,offset,payload) list
with postings format.
*/
type Lucene41PostingsReader struct {
	docIn   store.IndexInput
	posIn   store.IndexInput
	payIn   store.IndexInput
	forUtil ForUtil
	version int
}

func NewLucene41PostingsReader(dir store.Directory,
	fis FieldInfos, si *SegmentInfo,
	ctx store.IOContext, segmentSuffix string) (r PostingsReaderBase, err error) {

	log.Print("Initializing Lucene41PostingsReader...")
	success := false
	var docIn, posIn, payIn store.IndexInput = nil, nil, nil
	defer func() {
		if !success {
			log.Print("Failed to initialize Lucene41PostingsReader.")
			if err != nil {
				log.Print("DEBUG ", err)
			}
			util.CloseWhileSuppressingError(docIn, posIn, payIn)
		}
	}()

	docIn, err = dir.OpenInput(util.SegmentFileName(si.Name, segmentSuffix, LUCENE41_DOC_EXTENSION), ctx)
	if err != nil {
		return nil, err
	}
	var version int32
	version, err = codec.CheckHeader(docIn, LUCENE41_DOC_CODEC, LUCENE41_VERSION_START, LUCENE41_VERSION_CURRENT)
	if err != nil {
		return nil, err
	}
	forUtil, err := NewForUtilFrom(docIn)
	if err != nil {
		return nil, err
	}

	if fis.HasProx {
		posIn, err = dir.OpenInput(util.SegmentFileName(si.Name, segmentSuffix, LUCENE41_POS_EXTENSION), ctx)
		if err != nil {
			return nil, err
		}
		_, err = codec.CheckHeader(posIn, LUCENE41_POS_CODEC, version, version)
		if err != nil {
			return nil, err
		}

		if fis.HasPayloads || fis.HasOffsets {
			payIn, err = dir.OpenInput(util.SegmentFileName(si.Name, segmentSuffix, LUCENE41_PAY_EXTENSION), ctx)
			if err != nil {
				return nil, err
			}
			_, err = codec.CheckHeader(payIn, LUCENE41_PAY_CODEC, version, version)
			if err != nil {
				return nil, err
			}
		}
	}

	success = true
	return &Lucene41PostingsReader{docIn, posIn, payIn, forUtil, int(version)}, nil
}

func (r *Lucene41PostingsReader) Init(termsIn store.IndexInput) error {
	log.Printf("Initializing from: %v", termsIn)
	// Make sure we are talking to the matching postings writer
	_, err := codec.CheckHeader(termsIn, LUCENE41_TERMS_CODEC, LUCENE41_VERSION_START, LUCENE41_VERSION_CURRENT)
	if err != nil {
		return err
	}
	indexBlockSize, err := termsIn.ReadVInt()
	if err != nil {
		return err
	}
	log.Printf("Index block size: %v", indexBlockSize)
	if indexBlockSize != LUCENE41_BLOCK_SIZE {
		panic(fmt.Sprintf("index-time BLOCK_SIZE (%v) != read-time BLOCK_SIZE (%v)", indexBlockSize, LUCENE41_BLOCK_SIZE))
	}
	return nil
}

/**
 * Read values that have been written using variable-length encoding instead of bit-packing.
 */
func readVIntBlock(docIn store.IndexInput, docBuffer []int,
	freqBuffer []int, num int, indexHasFreq bool) (err error) {
	if indexHasFreq {
		for i := 0; i < num; i++ {
			code, err := asInt(docIn.ReadVInt())
			if err != nil {
				return err
			}
			docBuffer[i] = int(uint(code) >> 1)
			if (code & 1) != 0 {
				freqBuffer[i] = 1
			} else {
				freqBuffer[i], err = asInt(docIn.ReadVInt())
				if err != nil {
					return err
				}
			}
		}
	} else {
		for i := 0; i < num; i++ {
			docBuffer[i], err = asInt(docIn.ReadVInt())
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func asInt(n int32, err error) (int, error) {
	return int(n), err
}

func (r *Lucene41PostingsReader) NewTermState() *BlockTermState {
	return newIntBlockTermState().BlockTermState
}

func (r *Lucene41PostingsReader) Close() error {
	return util.Close(r.docIn, r.posIn, r.payIn)
}

func (r *Lucene41PostingsReader) DecodeTerm(longs []int64,
	in util.DataInput, fieldInfo *FieldInfo,
	_termState *BlockTermState, absolute bool) error {
	panic("not implemented yet")
}

func (r *Lucene41PostingsReader) Docs(fieldInfo *FieldInfo,
	termState *BlockTermState, liveDocs util.Bits,
	reuse DocsEnum, flags int) (de DocsEnum, err error) {

	var docsEnum *blockDocsEnum
	if v, ok := reuse.(*blockDocsEnum); ok {
		docsEnum = v
		if !docsEnum.canReuse(r.docIn, fieldInfo) {
			docsEnum = newBlockDocsEnum(r, fieldInfo)
		}
	} else {
		docsEnum = newBlockDocsEnum(r, fieldInfo)
	}
	return docsEnum.reset(liveDocs, termState.Self.(*intBlockTermState), flags)
}

type blockDocsEnum struct {
	*Lucene41PostingsReader // embedded struct

	encoded []byte

	docDeltaBuffer []int
	freqBuffer     []int

	docBufferUpto int

	// skipper Lucene41SkipReader
	skipped bool

	startDocIn store.IndexInput

	docIn            store.IndexInput
	indexHasFreq     bool
	indexHasPos      bool
	indexHasOffsets  bool
	indexHasPayloads bool

	docFreq       int
	totalTermFreq int64
	docUpto       int
	doc           int
	accum         int
	freq          int

	// Where this term's postings start in the .doc file:
	docTermStartFP int64

	// Where this term's skip data starts (after
	// docTermStartFP) in the .doc file (or -1 if there is
	// no skip data for this term):
	skipOffset int64

	// docID for next skip point, we won't use skipper if
	// target docID is not larger than this
	nextSkipDoc int

	liveDocs util.Bits

	needsFreq      bool
	singletonDocID int
}

func newBlockDocsEnum(owner *Lucene41PostingsReader,
	fieldInfo *FieldInfo) *blockDocsEnum {

	return &blockDocsEnum{
		Lucene41PostingsReader: owner,
		docDeltaBuffer:         make([]int, MAX_DATA_SIZE),
		freqBuffer:             make([]int, MAX_DATA_SIZE),
		startDocIn:             owner.docIn,
		docIn:                  nil,
		indexHasFreq:           fieldInfo.IndexOptions() >= INDEX_OPT_DOCS_AND_FREQS,
		indexHasPos:            fieldInfo.IndexOptions() >= INDEX_OPT_DOCS_AND_FREQS_AND_POSITIONS,
		indexHasOffsets:        fieldInfo.IndexOptions() >= INDEX_OPT_DOCS_AND_FREQS_AND_POSITIONS,
		indexHasPayloads:       fieldInfo.HasPayloads(),
		encoded:                make([]byte, MAX_ENCODED_SIZE),
	}
}

func (de *blockDocsEnum) canReuse(docIn store.IndexInput, fieldInfo *FieldInfo) bool {
	return docIn == de.startDocIn &&
		de.indexHasFreq == (fieldInfo.IndexOptions() >= INDEX_OPT_DOCS_AND_FREQS) &&
		de.indexHasPos == (fieldInfo.IndexOptions() >= INDEX_OPT_DOCS_AND_FREQS_AND_POSITIONS) &&
		de.indexHasPayloads == fieldInfo.HasPayloads()
}

func (de *blockDocsEnum) reset(liveDocs util.Bits, termState *intBlockTermState, flags int) (ret DocsEnum, err error) {
	de.liveDocs = liveDocs
	log.Printf("  FPR.reset: termState=%v", termState)
	de.docFreq = termState.DocFreq
	if de.indexHasFreq {
		de.totalTermFreq = termState.TotalTermFreq
	} else {
		de.totalTermFreq = int64(de.docFreq)
	}
	de.docTermStartFP = termState.docStartFP // <---- docTermStartFP should be 178 instead of 0
	de.skipOffset = termState.skipOffset
	de.singletonDocID = termState.singletonDocID
	if de.docFreq > 1 {
		if de.docIn == nil {
			// lazy init
			de.docIn = de.startDocIn.Clone()
		}
		err = de.docIn.Seek(de.docTermStartFP)
		if err != nil {
			return nil, err
		}
	}

	de.doc = -1
	de.needsFreq = (flags & DOCS_ENUM_FLAG_FREQS) != 0
	if !de.indexHasFreq {
		for i, _ := range de.freqBuffer {
			de.freqBuffer[i] = 1
		}
	}
	de.accum = 0
	de.docUpto = 0
	de.nextSkipDoc = LUCENE41_BLOCK_SIZE - 1 // we won't skip if target is found in first block
	de.docBufferUpto = LUCENE41_BLOCK_SIZE
	de.skipped = false
	return de, nil
}

func (de *blockDocsEnum) Freq() (n int, err error) {
	return de.freq, nil
}

func (de *blockDocsEnum) DocId() int {
	return de.doc
}

func (de *blockDocsEnum) refillDocs() (err error) {
	left := de.docFreq - de.docUpto
	assert(left > 0)

	if left >= LUCENE41_BLOCK_SIZE {
		log.Printf("    fill doc block from fp=%v", de.docIn.FilePointer())
		panic("not implemented yet")
	} else if de.docFreq == 1 {
		de.docDeltaBuffer[0] = de.singletonDocID
		de.freqBuffer[0] = int(de.totalTermFreq)
	} else {
		// Read vInts:
		log.Printf("    fill last vInt block from fp=%v", de.docIn.FilePointer())
		err = readVIntBlock(de.docIn, de.docDeltaBuffer, de.freqBuffer, left, de.indexHasFreq)
	}
	de.docBufferUpto = 0
	return
}

func (de *blockDocsEnum) NextDoc() (n int, err error) {
	log.Println("FPR.nextDoc")
	for {
		log.Printf("  docUpto=%v (of df=%v) docBufferUpto=%v", de.docUpto, de.docFreq, de.docBufferUpto)

		if de.docUpto == de.docFreq {
			log.Println("  return doc=END")
			de.doc = NO_MORE_DOCS
			return de.doc, nil
		}

		if de.docBufferUpto == LUCENE41_BLOCK_SIZE {
			err = de.refillDocs()
			if err != nil {
				return 0, err
			}
		}

		log.Printf("    accum=%v docDeltaBuffer[%v]=%v", de.accum, de.docBufferUpto, de.docDeltaBuffer[de.docBufferUpto])
		de.accum += de.docDeltaBuffer[de.docBufferUpto]
		de.docUpto++

		if de.liveDocs == nil || de.liveDocs.At(de.accum) {
			de.doc = de.accum
			de.freq = de.freqBuffer[de.docBufferUpto]
			de.docBufferUpto++
			log.Printf("  return doc=%v freq=%v", de.doc, de.freq)
			return de.doc, nil
		}
		log.Printf("  doc=%v is deleted; try next doc", de.accum)
		de.docBufferUpto++
	}
}

func (de *blockDocsEnum) Advance(target int) (int, error) {
	// TODO: make frq block load lazy/skippable
	fmt.Printf("  FPR.advance target=%v\n", target)

	// current skip docID < docIDs generated from current buffer <= next
	// skip docID, we don't need to skip if target is buffered already
	if de.docFreq > LUCENE41_BLOCK_SIZE && target > de.nextSkipDoc {
		fmt.Println("load skipper")

		panic("not implemented yet")
	}
	if de.docUpto == de.docFreq {
		de.doc = NO_MORE_DOCS
		return de.doc, nil
	}
	if de.docBufferUpto == LUCENE41_BLOCK_SIZE {
		err := de.refillDocs()
		if err != nil {
			return 0, nil
		}
	}

	// Now scan.. this is an inlined/pared down version of nextDoc():
	for {
		fmt.Printf("  scan doc=%v docBufferUpto=%v\n", de.accum, de.docBufferUpto)
		de.accum += de.docDeltaBuffer[de.docBufferUpto]
		de.docUpto++

		if de.accum >= target {
			break
		}
		de.docBufferUpto++
		if de.docUpto == de.docFreq {
			de.doc = NO_MORE_DOCS
			return de.doc, nil
		}
	}

	if de.liveDocs == nil || de.liveDocs.At(de.accum) {
		fmt.Printf("  return doc=%v\n", de.accum)
		de.freq = de.freqBuffer[de.docBufferUpto]
		de.docBufferUpto++
		de.doc = de.accum
		return de.doc, nil
	} else {
		fmt.Println("  now do nextDoc()")
		de.docBufferUpto++
		return de.NextDoc()
	}
}