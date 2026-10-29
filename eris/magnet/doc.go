// Package magnet parses and creates Magnet URIs that identify ERIS content.
//
// An ERIS Magnet URI has an ERIS read capability in its exact-topic (xt)
// parameter. It can also have a display name (dn), an exact length (xl), and
// extension parameters. This package does not read content or access a network.
//
// See the [ERIS Magnet recommendation].
//
// [ERIS Magnet recommendation]: https://eris.codeberg.page/eer/magnet-uri.xml
package magnet
