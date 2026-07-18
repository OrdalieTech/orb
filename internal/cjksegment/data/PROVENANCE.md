# ICU CJK word-break data

`cjdict.dict` is copied byte-for-byte from Unicode ICU release 78.2:

- repository: `https://github.com/unicode-org/icu`
- tag: `release-78.2`
- commit: `f1b3db8ecd39d5b3a6eff4d5641b176c7f914dfb`
- source path: `icu4j/main/core/src/main/resources/com/ibm/icu/impl/data/icudata/brkitr/cjdict.dict`
- source dictionary: `icu4c/source/data/brkitr/dictionaries/cjdict.txt`
- generator: `icu4c/source/tools/gendict/gendict.cpp --uchars`
- size: `2,007,296` bytes
- SHA-256: `5b96312a434f4ca3df1f5fa906e88d52fe2e28e3b87c68b9e62d0d77e1995edc`

The adjacent `LICENSE` is the unmodified 27,718-byte ICU 78.2 root license file. Its SHA-256 is
`e55522d81edc687a341a4411e0776e54ca654e90147f354a90458aaced4116af`. Its Unicode-3.0 notice
covers ICU code and data, and its “Chinese/Japanese Word Break Dictionary Data” section carries
the additional Google, Libtabe, IPADIC/NAIST, and ICOT notices required for `cjdict` redistribution.

The pure-Go reader follows `icu4c/source/common/{dictionarydata.cpp,dictbe.cpp}` and the UCharsTrie
1.0 format implemented by `icu4j/main/core/src/main/java/com/ibm/icu/util/CharsTrie.java` at the same
release commit. It deliberately accepts only the big-endian `Dict` v1 UCharsTrie-with-values form
used by this pinned asset; there is no compatibility path for older ICU dictionary formats.
