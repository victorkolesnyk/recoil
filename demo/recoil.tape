# VHS tape for the recoil README demo.
# Install VHS (https://github.com/charmbracelet/vhs), then from the repo root:
#   vhs demo/recoil.tape
# It writes assets/demo.gif — commit that and uncomment the image in README.md.
Output assets/demo.gif

Set Shell "bash"
Set FontSize 18
Set Width 1100
Set Height 640
Set Padding 24

Type "recoil init"
Enter
Sleep 800ms

Type `recoil encode --trigger test-fail --gist "Don't name a Unity folder Build/, .gitignore untracks it" --cue "unity build folder gitignore"`
Enter
Sleep 1200ms

Type `echo "editing .gitignore and a new Build dir" | recoil recall`
Enter
Sleep 2500ms

Type "recoil guard --files .gitignore"
Enter
Sleep 2500ms
