#!/usr/bin/env python3
"""Normalize a `docker save` tar for `kind load image-archive`.

Docker with the containerd image store (default on recent GitHub runners)
exports the OCI index with descriptors for platforms/attestations whose
blobs were never pulled. `kind load` shells out to `ctr images import
--digests`, which hard-fails on such dangling descriptors:

    ctr: content digest sha256:...: not found

This script rewrites the tar's OCI index.json so every descriptor it
references is fully present in the tar:

- image manifests whose config/layers are missing are dropped;
- nested indexes (multi-platform manifests) are flattened: children that
  fully resolve are hoisted to the top level (inheriting the parent's
  name annotation so the image name still registers), children that
  dangle are dropped — nested indexes are content-addressed and cannot be
  edited in place;
- attestations (buildx provenance, in-toto configs, unknown/unknown
  platforms) are dropped even when structurally complete — they are not
  runnable images and ctr unpack fails on them with 'mismatched image
  rootfs and manifest layers'.

The docker-format manifest.json/repositories files are kept untouched for
`docker load` consumers. A tar with no dangling content is left unchanged.

Usage: normalize-image-tar.py <image.tar> [<image2.tar> ...]
Exit code is 0 on success. If no index entry resolves at all (e.g. a
retagged single image whose index references unexported content), the tar
is left unchanged with a warning — docker-format consumers (`docker load`)
are unaffected by OCI index problems.
"""

import json
import os
import shutil
import sys
import tarfile
import tempfile

NAME_ANNOTATION = "io.containerd.image.name"
REF_ANNOTATION = "org.opencontainers.image.ref.name"


def normalize(path):
    with tarfile.open(path) as tf:
        members = {m.name: m for m in tf.getmembers() if m.isfile()}
        blobs = {n.rsplit("/", 1)[1] for n in members if n.startswith("blobs/sha256/")}

        def load_json(name):
            return json.load(tf.extractfile(name))

        cache = {}

        def doc_of(digest):
            hexd = digest.split(":", 1)[1]
            if hexd not in blobs:
                return None
            if digest not in cache:
                cache[digest] = json.load(tf.extractfile(f"blobs/sha256/{hexd}"))
            return cache[digest]

        def image_manifest_complete(digest):
            doc = doc_of(digest)
            if doc is None or "manifests" in doc:
                return False
            refs = []
            if "config" in doc:
                refs.append(doc["config"]["digest"])
            refs += [l["digest"] for l in doc.get("layers", [])]
            return all(r.split(":", 1)[1] in blobs for r in refs)

        def is_attestation(desc):
            """buildx attestation manifests are structurally complete but not
            runnable: ctr unpack fails with 'mismatched image rootfs and
            manifest layers' (in-toto config has empty rootfs diff_ids)."""
            annotations = desc.get("annotations") or {}
            if annotations.get("vnd.docker.reference.type") == "attestation-manifest":
                return True
            platform = desc.get("platform") or {}
            if platform.get("architecture") == "unknown" and platform.get("os") == "unknown":
                return True
            doc = doc_of(desc["digest"])
            if doc and "config" in doc:
                if "in-toto" in str(doc["config"].get("mediaType", "")):
                    return True
            return False

        def resolve(desc, inherited_annotations):
            """Return a list of top-level-safe descriptors for desc."""
            doc = doc_of(desc["digest"])
            if doc is None or is_attestation(desc):
                return []
            annotations = dict(inherited_annotations)
            annotations.update(desc.get("annotations") or {})
            if "manifests" in doc:  # nested index: flatten
                out = []
                for child in doc["manifests"]:
                    out += resolve(child, annotations)
                return out
            if image_manifest_complete(desc["digest"]):
                kept = dict(desc)
                kept["annotations"] = annotations
                return [kept]
            return []

        index = load_json("index.json")
        kept, dropped = [], []
        for desc in index.get("manifests", []):
            resolved = resolve(desc, {})
            if resolved:
                kept += resolved
            else:
                ann = desc.get("annotations") or {}
                plat = desc.get("platform") or {}
                name = ann.get(NAME_ANNOTATION, desc["digest"])
                if is_attestation(desc):
                    name += " (attestation)"
                elif plat:
                    name += f" ({plat.get('os')}/{plat.get('architecture')})"
                dropped.append(name)

        if dropped and not kept:
            # Nothing resolvable via the OCI index — e.g. a retagged single
            # image whose index points at unexported content. Consumers using
            # the docker-format manifest.json (docker load) are unaffected by
            # index issues, so warn and leave the tar untouched.
            print(f"{path}: WARNING: no resolvable OCI index entries: {dropped}; leaving tar unchanged", file=sys.stderr)
            return 0

        # No-op fast path: identical descriptor set, nothing hoisted.
        if [d["digest"] for d in kept] == [d["digest"] for d in index.get("manifests", [])]:
            print(f"{path}: OK ({len(kept)} entries, nothing dangling)")
            return 0

        if dropped:
            print(f"{path}: dropping unresolvable index entries: {dropped}")
        index["manifests"] = kept

        fd, tmp = tempfile.mkstemp(suffix=".tar", dir=os.path.dirname(path) or ".")
        os.close(fd)
        try:
            with tarfile.open(path) as src, tarfile.open(tmp, "w") as dst:
                for m in src.getmembers():
                    if m.name == "index.json":
                        continue
                    f = src.extractfile(m) if m.isfile() else None
                    dst.addfile(m, f)
                payload = json.dumps(index).encode()
                info = tarfile.TarInfo("index.json")
                info.size = len(payload)
                with tempfile.SpooledTemporaryFile() as buf:
                    buf.write(payload)
                    buf.seek(0)
                    dst.addfile(info, buf)
            shutil.move(tmp, path)
        except BaseException:
            os.unlink(tmp)
            raise
        print(f"{path}: normalized ({len(kept)} entries kept)")
        return 0


def main():
    rc = 0
    for path in sys.argv[1:]:
        rc |= normalize(path)
    return rc


if __name__ == "__main__":
    sys.exit(main())
