package main

// linuxAppID is the identity the Linux desktop uses for the app. It is passed
// to GTK as the program name (linux.Options.ProgramName), which becomes the
// Wayland app_id, and it names the self-installed desktop entry, so the
// launcher, window grouping and taskbar icon all resolve to this app.
const linuxAppID = "starlance-replay-uploader"
