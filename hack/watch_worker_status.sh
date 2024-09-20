#!/bin/sh

refresh() {
  tput clear || exit 2; # Clear screen. Almost same as echo -en '\033[2J';
  bash -ic "$@";
}

# Like watch, but with color
cwatch() {
   while true; do
     CMD="$@";
     # Cache output to prevent flicker. Assigning to variable
     # also removes trailing newline.
     output=`refresh "$CMD"`;
     # Exit if ^C was pressed while command was executing or there was an error.
     exitcode=$?; [ $exitcode -ne 0 ] && exit $exitcode
     printf '%s' "$output";  # Almost the same as echo $output
     sleep 5;
   done;
}

cwatch "kubectl get -o jsonpath=\"{.status}\" temporalworker $1 | yq -p json -o yaml | grep -v -E 'apiVersion|resourceVersion|kind|uid|namespace|deployment|name|versionConflictToken|healthySince' | bat -l yaml --color always --plain --wrap never --theme gruvbox-dark"
