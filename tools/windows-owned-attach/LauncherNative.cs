using System;
using System.Runtime.InteropServices;

namespace Tether.OwnedAttach {
    // Additional declarations for this fixed disposable-host experiment only.
    internal static class LauncherNative {
        [DllImport("kernel32.dll", SetLastError = true)]
        internal static extern bool CreatePipe(out IntPtr read, out IntPtr write,
            IntPtr attributes, uint size);
        [DllImport("kernel32.dll", SetLastError = true)]
        internal static extern bool GetHandleInformation(IntPtr handle, out uint flags);
        [DllImport("kernel32.dll", SetLastError = true)]
        internal static extern bool SetHandleInformation(IntPtr handle, uint mask, uint flags);
        [DllImport("kernel32.dll", SetLastError = true)]
        internal static extern bool PeekNamedPipe(IntPtr pipe, IntPtr buffer, uint size,
            IntPtr read, out uint available, IntPtr left);
        [DllImport("kernel32.dll", SetLastError = true)]
        internal static extern bool ReadFile(IntPtr file, [Out] byte[] buffer, uint size,
            out uint read, IntPtr overlapped);
        [DllImport("kernel32.dll", SetLastError = true)]
        internal static extern bool WriteFile(IntPtr file, byte[] buffer, uint size,
            out uint written, IntPtr overlapped);
    }
}
