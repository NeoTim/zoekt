// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package zoekt

import (
	"bytes"
	"fmt"
	"github.com/google/zoekt/query"
	"log"
	"strings"
)

var _ = log.Println

// indexData holds the pattern independent data that we have to have
// in memory to search.
type indexData struct {
	reader *reader

	ngrams map[ngram]simpleSection

	postingsIndex []uint32
	newlinesIndex []uint32
	caseBitsIndex []uint32

	// offsets of file contents. Includes end of last file.
	boundaries []uint32

	fileEnds []uint32

	fileNameContent       []byte
	fileNameCaseBits      []byte
	fileNameCaseBitsIndex []uint32
	fileNameIndex         []uint32
	fileNameNgrams        map[ngram][]uint32

	fileBranchMasks []uint32
	branchNames     map[int]string
	branchIDs       map[string]int

	repoName string
}

func (data *indexData) getDocIterator(query *query.Substring) (docIterator, error) {
	if query.FileName {
		return data.getFileNameDocIterator(query), nil
	}
	if len(query.Pattern) < ngramSize {
		return nil, fmt.Errorf("pattern %q less than %d bytes", query.Pattern, ngramSize)
	}

	return data.getContentDocIterator(query)
}

type bruteForceIter struct {
	cands []*candidateMatch
}

func (i *bruteForceIter) next() []*candidateMatch {
	return i.cands
}
func (i *bruteForceIter) coversContent() bool {
	return true
}

func (data *indexData) getBruteForceFileNameDocIterator(query *query.Substring) docIterator {
	lowerStr := toLower([]byte(query.Pattern))
	last := uint32(0)

	var cands []*candidateMatch
	for i, off := range data.fileNameIndex[1:] {
		name := data.fileNameContent[last:off]
		last = off
		idx := bytes.Index(name, lowerStr)
		if idx == -1 {
			continue
		}
		cands = append(cands, &candidateMatch{
			caseSensitive: query.CaseSensitive,
			fileName:      true,
			substrBytes:   []byte(query.Pattern),
			substrLowered: lowerStr,
			file:          uint32(i),
			offset:        uint32(idx),
			matchSz:       uint32(len(lowerStr)),
		})
	}

	return &bruteForceIter{cands}
}

func (data *indexData) getFileNameDocIterator(query *query.Substring) docIterator {
	if len(query.Pattern) < ngramSize {
		return data.getBruteForceFileNameDocIterator(query)
	}
	str := strings.ToLower(query.Pattern) // TODO - UTF-8
	di := &ngramDocIterator{
		query:    query,
		distance: uint32(len(str)) - ngramSize,
		ends:     data.fileNameIndex[1:],
		first:    data.fileNameNgrams[stringToNGram(str[:ngramSize])],
		last:     data.fileNameNgrams[stringToNGram(str[len(str)-ngramSize:])],
	}

	return di
}

const maxUInt32 = 0xffffffff

func minarg(xs []uint32) uint32 {
	m := uint32(maxUInt32)
	j := len(xs)
	for i, x := range xs {
		if x <= m {
			m = x
			j = i
		}
	}
	return uint32(j)
}

func (data *indexData) getContentDocIterator(query *query.Substring) (docIterator, error) {
	str := strings.ToLower(query.Pattern) // TODO - UTF-8
	input := &ngramDocIterator{
		query: query,
		ends:  data.fileEnds,
	}

	// Find the 2 least common ngrams from the string.
	frequencies := make([]uint32, len(str)-ngramSize+1)
	for i := range frequencies {
		frequencies[i] = data.ngrams[stringToNGram(str[i:i+ngramSize])].sz
		if frequencies[i] == 0 {
			return input, nil
		}
	}

	firstI := minarg(frequencies)
	frequencies[firstI] = maxUInt32
	lastI := minarg(frequencies)
	if firstI > lastI {
		lastI, firstI = firstI, lastI
	}

	first := data.ngrams[stringToNGram(str[firstI:firstI+ngramSize])]
	last := data.ngrams[stringToNGram(str[lastI:lastI+ngramSize])]
	input.distance = lastI - firstI
	input.leftPad = firstI
	input.rightPad = uint32(len(str)-ngramSize) - lastI

	input.first = fromDeltas(data.reader.readSectionBlob(first))
	if data.reader.err != nil {
		return nil, data.reader.err
	}

	if firstI != lastI {
		input.last = fromDeltas(data.reader.readSectionBlob(last))
		if data.reader.err != nil {
			return nil, data.reader.err
		}
	} else {
		input.last = input.first
	}

	if lastI-firstI <= ngramSize && input.leftPad == 0 && input.rightPad == 0 {
		input._coversContent = true
	}
	return input, nil
}

func (d *indexData) fileName(i uint32) string {
	return string(d.fileNameContent[d.fileNameIndex[i]:d.fileNameIndex[i+1]])
}

func (s *indexData) Close() error {
	return s.reader.r.Close()
}
