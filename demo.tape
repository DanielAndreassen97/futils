# Render with: vhs demo.tape
#
# Self-contained: a hidden setup block seeds a sandbox config (FUTILS_CONFIG)
# with a fictional "Fabrikam" customer, so the demo never touches a real
# config or tenant. Everything shown is offline — no auth, no API calls.
#
# Flow: main menu → Deploy (environment picker with mappings) → back →
# Manage customers → Edit customer (Deploy setup group + ? info box) → quit.

Output demo.gif

Set Shell zsh
Set FontSize 14
Set Width 900
Set Height 620
Set Padding 20
Set Theme "Catppuccin Mocha"

# --- hidden sandbox setup (not recorded) ---------------------------------
# `futils demoseed` owns the sandbox content (config, Fabrikam repo, origin) —
# hand-rolling the JSON here drifted from what demoseed generates.
Hide
Type `futils demoseed >/dev/null && export FUTILS_CONFIG=/tmp/futils-demo/config.json && clear`
Enter
Sleep 1s
Show

# --- banner + main menu ---------------------------------------------------
Type "futils"
Sleep 0.5s
Enter
Sleep 3s

# --- Deploy: environment picker shows the folder→workspace mappings ------
Type "4"
Sleep 3s
Escape
Sleep 1.5s

# --- Edit customer: the Deploy setup group --------------------------------
Type "6"
Sleep 1s
Type "2"
Sleep 1s
Type "1"
Sleep 2.5s

# highlight "Baseline environment" and open its ? info box
# (six rows down: DEV, TEST, PROD, Add env, Primary repo path, Deploy branch)
Down
Sleep 0.3s
Down
Sleep 0.3s
Down
Sleep 0.3s
Down
Sleep 0.3s
Down
Sleep 0.3s
Down
Sleep 0.5s
Type "?"
Sleep 4s

# --- back to the main menu, quit ------------------------------------------
Type "m"
Sleep 1.5s
Type "q"
Sleep 1s
