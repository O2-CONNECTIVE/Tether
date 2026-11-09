using System;
using System.Runtime.InteropServices;
using System.Text;

namespace Tether.OwnerProbe {
    internal static class DescendantNative {
        [StructLayout(LayoutKind.Sequential)] internal struct StartupEx {
            internal Native.Startup startup;
            internal IntPtr attributes;
        }
        [DllImport("kernel32.dll")] internal static extern IntPtr GetCurrentProcess();
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool DuplicateHandle(IntPtr sourceProcess, IntPtr source,
            IntPtr targetProcess, out IntPtr target, uint access, bool inherit, uint options);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool InitializeProcThreadAttributeList(IntPtr list, int count,
            uint flags, ref IntPtr size);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool UpdateProcThreadAttribute(IntPtr list, uint flags,
            IntPtr attribute, IntPtr value, IntPtr size, IntPtr previous, IntPtr returned);
        [DllImport("kernel32.dll")]
        internal static extern void DeleteProcThreadAttributeList(IntPtr list);
        [DllImport("kernel32.dll", SetLastError=true, CharSet=CharSet.Unicode)]
        internal static extern bool CreateProcessW(string image, StringBuilder command,
            IntPtr processAttributes, IntPtr threadAttributes, bool inherit, uint flags,
            IntPtr environment, string cwd, ref StartupEx startup, out Native.ProcessInfo process);
    }
}
