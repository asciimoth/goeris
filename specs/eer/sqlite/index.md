## title: SQLite for ERIS block storage synopsis: Store blocks into a portable SQLite database

[SQLite](https://sqlite.org) is a popular embedded database. This document
specifies a portable schema for storing blocks of ERIS encoded content into an
SQLite database. This allows SQLite datbases to be used as portable,
cross-platform and cross-implementation formats for storing and exchanging ERIS
encoded content (see also
[SQLite As An Application File Format](https://www.sqlite.org/appfileformat.html)).

An SQLite database is well-suited for transferring ERIS encoded content over USB
sticks or other portable media. Unlike the
[CBOR serialization](https://eris.codeberg.page/eer/cbor.xml) an SQLite database
allows efficient random-access without additional indexing or decoding.

## Schema

Blocks of ERIS encoded content should be stored in a table `eris_block` that has
3 columns:

- `block_id`: An integer identifier of the block. This is a database internal
  identifier that can be used to reference blocks from other tables in the
  database (see section on extensibility below). There is practically no
  performance cost for having this columns as it is an alias of the SQLite
  `rowid` column (see
  [the SQLite documentation on ROWID](https://www.sqlite.org/lang_createtable.html#rowid)).
- `ref`: Stores the binary block reference (the Blake2b-256 hash of the block)
  with type `BLOB`. The column is set as `PRIMARY KEY` ensuring uniqueness of
  blocks as well as allowing efficient access by reference.
- `block`: Stores the block contents with type `BLOB`

An SQL statement that creates the table and an index for accessing blocks by
their reference:

```sql
CREATE TABLE IF NOT EXISTS eris_block (
              block_id INTEGER PRIMARY KEY,
			  ref BLOB UNIQUE,
              block BLOB
);
```

## Implementation Notes

### Extensibility

Applications and implementations may define other tables in the SQLite database.
For example tables may be used for application specific configuration or block
management.

When referencing blocks in the `eris_block` table, the `block_id` columns should
be used as foreign key. This is much more efficient than using the `ref` column.

Note that it is necessary to define the `block_id` column instead of just using
the `rowid` column as SQLite does not allow the `rowid` to be a parent key (see
[the SQLite documentation on foreign key support](https://www.sqlite.org/foreignkeys.html)).

### Transactions

Performance can be increased by grouping block insertions into transactions (see
[the SQLite documentation on Transactions](https://www.sqlite.org/lang_transaction.html)).

## Implementations

The [Oebstly](https://codeberg.org/eris/oebstly) block storage tool implements
the SQLite schema as described above.
