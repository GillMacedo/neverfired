# 0003: Refuse input it can't trust, and only claim what is certain

**Decision:** neverfired exits 2 with nothing on stdout when a test file or rule file is missing, empty, binary, not YAML, cut off, the output of a failed command, has a misspelled field, an expression that doesn't parse, or tests nothing. A "this test can't fail" finding is made only when it holds for every possible sample value.

**Why:** the tool exists because promtool reports SUCCESS for tests that check nothing. A coverage tool that reports a clean result for a file it couldn't read, or flags a good test as useless, would repeat the problem in the other direction.

**Limits:** a file cut at the end of a line, right after a complete test case or rule, is a valid shorter file, and neither neverfired nor promtool can tell. The healthy example shows such cuts still don't come out clean, because the alerts that lose their tests are reported, but that depends on the file.
