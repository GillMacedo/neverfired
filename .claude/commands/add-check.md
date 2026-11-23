Add a neverfired check for: $ARGUMENTS

1. Write the smallest promtool test file and rule file that show the problem, and run `promtool test rules` on them to confirm what promtool does. Write a fixed version that must stay quiet.
2. Add both as a case in `tests/` with the codes they should get. If the check claims an expression returns nothing, run `make engine-check`.
3. Use the next unused code in its group (NF1xx tests that can't fail or are weak, NF2xx coverage, NF3xx notes). Never reuse a code.
4. Change `plugin/scripts`, add the code to the README table and the skill's list, run the check on a real suite such as kubernetes-mixin, then run `make test`.
