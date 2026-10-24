// The orb linked into the iOS app (platforms/ios/core): runs `orb app` over two pipes, in [home],
// until its input closes. Returns its exit code.
int OrbApp(const char *home, int in, int out);
