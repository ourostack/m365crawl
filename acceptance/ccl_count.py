#!/usr/bin/env python3
"""Counts Teams conversations and messages in a snapshot with the reference decoder.

usage: ccl_count.py <snapshot dir> [--keys-out FILE]

Prints one JSON object of counts. The snapshot holds leveldb/ and blob/ as m365crawl copies
them. ccl_chromium_reader comes from CCL_READER (default ~/code/_refs/ccl_chromium_reader) and
ccl_simplesnappy from CCL_SNAPPY (default ~/code/_refs/ccl_simplesnappy), both put on sys.path;
pip needs neither. brotli and zstd are only used for compressed Chromium caches, never for
IndexedDB, so empty stand-in modules satisfy their imports when the real ones are not installed.

--keys-out writes one line per conversation record version, "<key hash> <state> <seq> <file> <has value>",
for local diagnosis. Key hashes are SHA-256 prefixes, never ids.
"""
import hashlib
import json
import os
import sys
import types

home = os.path.expanduser("~")
for env, default in (("CCL_READER", "~/code/_refs/ccl_chromium_reader"), ("CCL_SNAPPY", "~/code/_refs/ccl_simplesnappy")):
    sys.path.insert(0, os.path.expanduser(os.environ.get(env, default)))
for mod in ("brotli", "zstd"):
    try:
        __import__(mod)
    except ImportError:
        sys.modules[mod] = types.ModuleType(mod)

from ccl_chromium_reader import ccl_chromium_indexeddb as idb  # noqa: E402

STORES = {
    "replychain-manager": "replychains-2",
    "conversation-manager": "conversations",
}


def key_id(key):
    v = key.value
    return v if isinstance(v, str) else repr(v)


def key_text(key):
    """Joins an array key's string parts with NUL, as the Go side does; any other key by repr."""
    v = key.value
    if isinstance(v, (tuple, list)):
        return "\0".join(str(getattr(x, "value", x)) for x in v)
    return v if isinstance(v, str) else repr(v)


def h12(text):
    return hashlib.sha256(text.encode()).hexdigest()[:12]


def main():
    snap = sys.argv[1]
    keys_out = sys.argv[sys.argv.index("--keys-out") + 1] if "--keys-out" in sys.argv else None
    db = idb.WrappedIndexDB(os.path.join(snap, "leveldb"), os.path.join(snap, "blob"))
    # key -> (sequence number, live, has value) of the newest version seen; chain key -> (seq, message hashes)
    conv_latest, chain_latest = {}, {}
    conv_versions = 0
    undecodable = set()  # hashes of record keys the reference could not decode
    undecodable_versions = {"conversation-manager": 0, "replychain-manager": 0}  # record versions it could not decode

    def note_undecodable(m, k):
        undecodable.add((m, h12(key_text(k))))
        undecodable_versions[m] += 1

    kf = open(keys_out, "w") if keys_out else None
    for dbid in db.database_ids:
        parts = dbid.name.split(":")
        if len(parts) < 5 or parts[0] != "Teams" or parts[2] != "react-web-client" or parts[1] not in STORES:
            continue
        manager = parts[1]
        store = db[dbid.dbid_no][STORES[manager]]
        for rec in store.iterate_records(bad_deserializer_data_handler=lambda k, v, m=manager: note_undecodable(m, k)):
            live = bool(rec.is_live)
            k = (dbid.name, key_id(rec.key))
            seq = rec.ldb_seq_no
            if manager == "conversation-manager":
                conv_versions += 1
                if k not in conv_latest or conv_latest[k][0] < seq:
                    conv_latest[k] = (seq, live, rec.value is not None)
                if kf:
                    kf.write(f"{h12(k[1])} {'live' if live else 'dead'} {seq} {os.path.basename(str(rec.origin_file))} {int(rec.value is not None)}\n")
            else:
                chain = rec.value
                mm = chain.get("messageMap") if hasattr(chain, "get") else None
                hashes = set()
                if mm:
                    conv = chain.get("conversationId", "")
                    for mv in mm.values():
                        mid = mv.get("id") if hasattr(mv, "get") else None
                        if mid is not None:
                            hashes.add(h12(str(mv.get("conversationId") or conv) + "\0" + str(mid)))
                if k not in chain_latest or chain_latest[k][0] < seq:
                    chain_latest[k] = (seq, live, hashes if live else set())
    live_convs = sorted({h12(k[1]) for k, v in conv_latest.items() if v[1] and v[2]})
    messages = set()
    for _, live, hashes in chain_latest.values():
        messages |= hashes
    out = {
        "reference": "ccl_chromium_reader",
        "conversations": {
            "record_versions": conv_versions,
            "distinct_keys": len(conv_latest),
            "latest_live": len(live_convs),
            "latest_tombstoned": len(conv_latest) - len(live_convs),
        },
        "messages": {"latest_distinct": len(messages), "chains": len(chain_latest)},
        "undecodable": {
            "conversations": sorted(h for m, h in undecodable if m == "conversation-manager"),
            "chains": sorted(h for m, h in undecodable if m == "replychain-manager"),
            "conversation_versions": undecodable_versions["conversation-manager"],
        },
        "hashes": {"conversations": live_convs, "messages": sorted(messages)},
    }
    print(json.dumps(out))


main()
