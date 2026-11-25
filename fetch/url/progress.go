package url

import (
	"fmt"
	"math/bits"

	"github.com/asciimoth/zpgo"
)

// ProgressNode holds a Zig progress label and item counts. Total is zero
// when the work has no known total. Child is nil when no download is active
// under this node.
type ProgressNode struct {
	Name             string
	Completed, Total uint64
	Child            *ProgressNode
}

// Progress receives a snapshot of the fetch progress tree. Calls are
// synchronous. Download counts update after each write to temporary storage.
// A fetch with one URL reports a download at the root. A fetch with mirrors
// reports checked candidates at the root and the active download as its child.
type Progress func(root ProgressNode)

// NodeProgress applies snapshots to a caller-owned Zig progress node. Use a
// separate node and adapter for each fetch. End the node when the fetch returns.
func NodeProgress(node zpgo.Node) Progress {
	var child zpgo.Node
	active := false
	update := func(node zpgo.Node, state ProgressNode) {
		node.SetName(state.Name)
		node.SetEstimatedTotalItems(state.Total)
		node.SetCompletedItems(state.Completed)
	}
	return func(root ProgressNode) {
		if root.Child != nil {
			if !active {
				child = node.Start(root.Child.Name, root.Child.Total)
				active = true
			}
			update(child, *root.Child)
		} else if active {
			child.End()
			active = false
		}
		// Ending a Zig child increments the parent. Use the snapshot count.
		update(node, root)
	}
}

func downloadProgress(downloaded uint64, length int64) ProgressNode {
	size := byteSize(downloaded)
	if length < 0 {
		return ProgressNode{Name: "Downloading " + size}
	}
	percent := uint64(100)
	if length > 0 {
		// Scale before rounding, with no overflow even for large downloads.
		high, low := bits.Mul64(downloaded, 100)
		percent, _ = bits.Div64(high, low, uint64(length))
	}
	return ProgressNode{
		Name:      fmt.Sprintf("Downloading (%s / %s)", size, byteSize(uint64(length))),
		Completed: percent,
		Total:     100,
	}
}

func byteSize(size uint64) string {
	if size < 1000 {
		return fmt.Sprintf("%d B", size)
	}
	units := [...]string{"KB", "MB", "GB", "TB", "PB", "EB"}
	scaled := float64(size) / 1000
	unit := 0
	for scaled >= 999.95 && unit < len(units)-1 {
		scaled /= 1000
		unit++
	}
	return fmt.Sprintf("%.1f %s", scaled, units[unit])
}
