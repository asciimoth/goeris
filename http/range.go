package http

import (
	"math"
	nethttp "net/http"
	"net/textproto"
	"strconv"
	"strings"
)

const maxRanges = 16

func tooManyRanges(values []string) bool {
	count := 0
	for _, value := range values {
		count += strings.Count(value, ",") + 1
		if count > maxRanges {
			return true
		}
	}
	return false
}

// ServeContent adds range lengths and multipart headers without checking for
// int64 overflow. Repeated subtrees can describe huge content with few blocks.
// Ignore ranges whose combined response is too large, as ServeContent does
// when their total exceeds the content size. Keep small ranges of huge content.
func safeRangeRequest(request *nethttp.Request, size int64) *nethttp.Request {
	value := request.Header.Get("Range")
	if size <= math.MaxInt64/(maxRanges+1) || !strings.Contains(value, ",") {
		return request
	}
	value, ok := strings.CutPrefix(value, "bytes=")
	if !ok {
		return request // Let ServeContent report malformed ranges.
	}
	// Reserve ample space for at most 16 MIME part headers and the boundary.
	remaining := min(size, math.MaxInt64-(1<<20))
	unsafe := false
	for entry := range strings.SplitSeq(value, ",") {
		entry = textproto.TrimString(entry)
		if entry == "" {
			continue
		}
		start, end, ok := strings.Cut(entry, "-")
		if !ok {
			return request
		}
		start, end = textproto.TrimString(start), textproto.TrimString(end)
		var length int64
		if start == "" {
			suffix, err := strconv.ParseInt(end, 10, 64)
			if err != nil || suffix < 0 || strings.HasPrefix(end, "-") {
				return request
			}
			length = min(suffix, size)
		} else {
			first, err := strconv.ParseInt(start, 10, 64)
			if err != nil || first < 0 {
				return request
			}
			if first >= size {
				continue
			}
			last := size - 1
			if end != "" {
				last, err = strconv.ParseInt(end, 10, 64)
				if err != nil || last < first {
					return request
				}
				last = min(last, size-1)
			}
			length = last - first + 1
		}
		if length > remaining {
			unsafe = true
		} else {
			remaining -= length
		}
	}
	if !unsafe {
		return request
	}
	clone := new(nethttp.Request)
	*clone = *request
	clone.Header = request.Header.Clone()
	clone.Header.Del("Range")
	return clone
}
