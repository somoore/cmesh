#!/usr/bin/env sh
set -eu

base_sha=${BASE_SHA:-}
head_sha=${HEAD_SHA:-}

if [ -z "$base_sha" ] || [ -z "$head_sha" ]; then
  echo false
  exit 0
fi

case "$base_sha" in
  0000000000000000000000000000000000000000)
    if git rev-parse -q --verify "$head_sha^" >/dev/null; then
      base_sha="$head_sha^"
    else
      echo false
      exit 0
    fi
    ;;
esac

git diff --name-status -z -M -C "$base_sha" "$head_sha" | perl -0 -e '
  local $/;
  my @fields = split /\0/, <STDIN>;
  pop @fields if @fields && $fields[-1] eq "";

  my $any = 0;
  while (@fields) {
    my $status = shift @fields;
    $any = 1;

    my @paths;
    if ($status =~ /^[RC]/) {
      print "false\n" and exit 0 if @fields < 2;
      push @paths, shift @fields, shift @fields;
    } else {
      print "false\n" and exit 0 if @fields < 1;
      push @paths, shift @fields;
    }

    for my $path (@paths) {
      print "false\n" and exit 0 if $path !~ /\.md\z/;
    }
  }

  print $any ? "true\n" : "false\n";
'
