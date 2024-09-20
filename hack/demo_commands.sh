#!/bin/sh

refresh() {
  tput clear || exit 2; # Clear screen. Almost same as echo -en '\033[2J';
  bash -ic "$@";
}

 while true; do
   CMD="$@";
   # Cache output to prevent flicker. Assigning to variable
   # also removes trailing newline.
   output=`refresh "$CMD"`;
   # Exit if ^C was pressed while command was executing or there was an error.
   exitcode=$?; [ $exitcode -ne 0 ] && exit $exitcode
   printf '%s' "$output";  # Almost the same as echo $output
   read -p "

[ press Enter to continue ]
" </dev/tty
 done;
