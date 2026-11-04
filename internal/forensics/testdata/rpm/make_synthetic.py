#!/usr/bin/env python3
"""Writes synthetic.rpmdb.sqlite.gz and synthetic.expected.tsv.

The file is written by SQLite itself (Python's sqlite3 module), with the
schema rpm 4.16+ uses for its Packages table and rpm header blobs in rpm's
on-disk layout. It exists to exercise what the real fixture
(ubi9-micro.rpmdb.sqlite.gz) does not: 512-byte pages, so the table and
the schema are b-trees several levels deep; headers far larger than a page,
so payloads run through long overflow chains; rows deleted after insertion,
so pages are freed and cells fragmented; and epochs.

    python3 -I make_synthetic.py   # from this directory
"""
import gzip
import os
import random
import sqlite3
import struct
import sys
import tempfile

TYPES = {"int32": 4, "string": 6, "bin": 7, "i18n": 9}


def header(fields):
    """An rpm header blob: il, dl, the entries, the data store."""
    store = bytearray()
    entries = []
    # rpm's immutable region marker comes first, as in a real header.
    entries.append((63, TYPES["bin"], None, 16))
    for tag, typ, value in fields:
        if typ == "int32":
            while len(store) % 4:
                store.append(0)
            entries.append((tag, TYPES[typ], len(store), 1))
            store += struct.pack(">I", value)
        else:
            entries.append((tag, TYPES[typ], len(store), 1))
            store += value.encode() + b"\0"
    region = len(store)
    store += b"\0" * 16
    out = bytearray(struct.pack(">II", len(entries), len(store)))
    for tag, typ, off, count in entries:
        out += struct.pack(">IIII", tag, typ, region if off is None else off, count)
    return bytes(out + store)


def tiny(here):
    """tiny.rpmdb.sqlite: a few rows, one through an overflow chain; the
    fuzz test's seed, small enough for mutations to reach the b-tree."""
    path = tempfile.mktemp(suffix=".sqlite")
    db = sqlite3.connect(path)
    db.execute("PRAGMA page_size = 512")
    db.execute("PRAGMA journal_mode = DELETE")
    db.execute("CREATE TABLE 'Packages' (hnum INTEGER PRIMARY KEY AUTOINCREMENT,blob BLOB NOT NULL)")
    for i, size in enumerate([10, 900, 10, 10]):
        db.execute("INSERT INTO Packages (blob) VALUES (?)", (header([(1000, "string", f"t{i}"), (1001, "string", "1"), (1002, "string", "1"), (1003, "int32", i), (1004, "i18n", "s" * size), (1022, "string", "noarch")]),))
    db.commit()
    db.close()
    with open(path, "rb") as f, open(os.path.join(here, "tiny.rpmdb.sqlite"), "wb") as out:
        out.write(f.read())
    os.unlink(path)


def main():
    rnd = random.Random(20261104)
    tiny(os.path.dirname(os.path.abspath(__file__)))
    path = tempfile.mktemp(suffix=".sqlite")
    db = sqlite3.connect(path)
    db.execute("PRAGMA page_size = 512")
    db.execute("PRAGMA journal_mode = DELETE")
    db.execute("CREATE TABLE 'Packages' (hnum INTEGER PRIMARY KEY AUTOINCREMENT,blob BLOB NOT NULL)")
    for name in ["Name", "Basenames", "Group", "Requirename", "Providename", "Dirnames", "Sigmd5", "Sha1header"]:
        db.execute(f"CREATE TABLE '{name}' (key 'TEXT' NOT NULL, hnum INTEGER NOT NULL, idx INTEGER NOT NULL, FOREIGN KEY (hnum) REFERENCES 'Packages'(hnum))")
        db.execute(f"CREATE INDEX '{name}_key_idx' ON '{name}'(key ASC)")
    rows = []
    for i in range(1500):
        name = f"pkg-{i:04d}"
        version = f"{rnd.randint(0, 9)}.{rnd.randint(0, 99)}"
        release = f"{rnd.randint(1, 30)}.fc40"
        arch = rnd.choice(["x86_64", "noarch", "i686"])
        lic = rnd.choice(["MIT", "GPL-2.0-or-later", "BSD-3-Clause", "LGPL-2.1-or-later AND MIT"])
        fields = [(1000, "string", name), (1001, "string", version), (1002, "string", release)]
        epoch = None
        if i % 7 == 0:
            epoch = rnd.randint(1, 3)
            fields.append((1003, "int32", epoch))
        # A summary (tag 1004), some of them far larger than a page.
        summary = "x" * (rnd.choice([10, 200, 3000]) if i % 50 else 40000)
        fields.append((1004, "i18n", summary))
        fields += [(1014, "string", lic), (1022, "string", arch)]
        db.execute("INSERT INTO Packages (blob) VALUES (?)", (header(fields),))
        db.execute("INSERT INTO Name VALUES (?, ?, 0)", (name, i + 1))
        rows.append((name, epoch, version, release, arch, lic))
    # Signing keys, as rpm lists them: no arch.
    db.execute("INSERT INTO Packages (blob) VALUES (?)", (header([(1000, "string", "gpg-pubkey"), (1001, "string", "a15b79cc"), (1002, "string", "63d04c2c"), (1014, "string", "pubkey")]),))
    rows.append(("gpg-pubkey", None, "a15b79cc", "63d04c2c", None, "pubkey"))
    # Remove every third package, as an erase would.
    for i in range(0, 1500, 3):
        db.execute("DELETE FROM Packages WHERE hnum = ?", (i + 1,))
    db.commit()
    db.close()
    kept = [r for i, r in enumerate(rows) if i >= 1500 or i % 3 != 0]
    here = os.path.dirname(os.path.abspath(__file__))
    with open(path, "rb") as f, gzip.GzipFile(os.path.join(here, "synthetic.rpmdb.sqlite.gz"), "wb", mtime=0) as out:
        out.write(f.read())
    os.unlink(path)
    with open(os.path.join(here, "synthetic.expected.tsv"), "w") as out:
        for name, epoch, version, release, arch, lic in sorted(kept):
            out.write("\t".join([name, "(none)" if epoch is None else str(epoch), version, release, arch or "(none)", lic]) + "\n")
    print(f"{len(kept)} packages", file=sys.stderr)


if __name__ == "__main__":
    main()
