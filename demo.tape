# Render with: vhs demo.tape
#
# Minimal demo that needs no live Fabric workspace — shows the banner,
# the help screen, and the version flag. Replace this with a fuller flow
# once a --demo mode (with fake customers/notebooks) is added.

Output demo.gif

Set Shell zsh
Set FontSize 14
Set Width 900
Set Height 520
Set Padding 20
Set Theme "Catppuccin Mocha"

# Show futils version
Type "futils --version"
Sleep 0.5s
Enter
Sleep 1.5s

# Show help
Type "futils help"
Sleep 0.5s
Enter
Sleep 3s

# Drop the user into the interactive menu so they see the banner
Type "futils"
Sleep 0.5s
Enter
Sleep 4s

# Quit cleanly
Type "q"
Sleep 1s
