package magnet_test

import (
	"fmt"

	"github.com/asciimoth/eris-tools/eris/magnet"
)

func ExampleParse() {
	uri, err := magnet.Parse("magnet:xt=urn:eris:BIAD77QDJMFAKZYH2DXBUZYAP3MXZ3DJZVFYQ5DFWC6T65WSFCU5S2IT4" +
		"YZGJ7AC4SYQMP2DM2ANS2ZTCP3DJJIRV733CRAAHOSWIYZM3M&dn=empty.txt&xl=0")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(uri.DisplayName)
	fmt.Println(*uri.ExactLength)
	// Output:
	// empty.txt
	// 0
}
