# Merges Go text coverage profiles written by `go test -coverprofile`.
#
# Usage: awk -f merge-go-coverprofiles.awk a.out b.out > merged.out
#
# Profiles must share one mode. Blocks are keyed by their `file:start,end`
# range and their counters combined: summed for count/atomic, maximised for
# set, which is the only combination `go tool cover` reads back correctly.
# Output order is first-seen, so the same inputs always produce the same file.

BEGIN {
  mode = ""
  blocks = 0
  failed = 0
}

function die(message) {
  print "merge-go-coverprofiles: " message > "/dev/stderr"
  failed = 1
  exit 1
}

FNR == 1 {
  if ($0 !~ /^mode: [a-z]+$/) {
    die(FILENAME ": missing or malformed mode header")
  }
  profile_mode = substr($0, 7)
  if (mode == "") {
    mode = profile_mode
  } else if (profile_mode != mode) {
    die(FILENAME ": mode \"" profile_mode "\" does not match \"" mode "\"")
  }
  next
}

NF != 3 || $1 !~ /^.+:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/ || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ {
  die(FILENAME ":" FNR ": malformed coverage line: " $0)
}

{
  if (!($1 in statements)) {
    statements[$1] = $2
    counters[$1] = 0
    order[blocks++] = $1
  } else if (statements[$1] != $2) {
    die(FILENAME ":" FNR ": block " $1 " has " $2 " statements, previously " statements[$1])
  }

  if (mode == "set") {
    if ($3 > counters[$1]) {
      counters[$1] = $3
    }
  } else {
    counters[$1] += $3
  }
}

END {
  if (failed) {
    exit 1
  }
  if (mode == "") {
    die("no coverage profile was given")
  }

  print "mode: " mode
  for (index_ = 0; index_ < blocks; index_++) {
    print order[index_], statements[order[index_]], counters[order[index_]]
  }
}
