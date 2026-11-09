using System;
using System.Runtime.InteropServices;
using System.Text;

namespace Tether.OwnerProbe {
    // Private API declarations for the disposable fixed-command CI probe, not product code.
    internal static class Native {
        [StructLayout(LayoutKind.Sequential)] internal struct Startup {
            internal uint cb;
            internal IntPtr reserved, desktop, title;
            internal uint x, y, xSize, ySize, xChars, yChars, fill, flags;
            internal ushort show, reservedSize;
            internal IntPtr reservedBytes, input, output, error;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct ProcessInfo {
            internal IntPtr process, thread;
            internal uint processId, threadId;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct BasicLimits {
            internal long processTime, jobTime;
            internal uint flags;
            internal UIntPtr minWorkingSet, maxWorkingSet;
            internal uint activeLimit;
            internal UIntPtr affinity;
            internal uint priority, scheduling;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct IoCounters {
            internal ulong readOps, writeOps, otherOps, readBytes, writeBytes, otherBytes;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct ExtendedLimits {
            internal BasicLimits basic;
            internal IoCounters io;
            internal UIntPtr processMemory, jobMemory, peakProcessMemory, peakJobMemory;
        }
        [StructLayout(LayoutKind.Sequential)] internal struct Accounting {
            internal long userTime, kernelTime, periodUserTime, periodKernelTime;
            internal uint faults, total, active, terminated;
        }
        [DllImport("kernel32.dll", SetLastError=true, CharSet=CharSet.Unicode)]
        internal static extern IntPtr CreateJobObjectW(IntPtr attributes, string name);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool SetInformationJobObject(IntPtr job, int kind, ref ExtendedLimits value, uint size);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool QueryInformationJobObject(IntPtr job, int kind, out Accounting value, uint size, IntPtr returned);
        [DllImport("kernel32.dll", SetLastError=true, CharSet=CharSet.Unicode)]
        internal static extern bool CreateProcessW(string image, StringBuilder command, IntPtr processAttributes,
            IntPtr threadAttributes, bool inherit, uint flags, IntPtr environment, string cwd,
            ref Startup startup, out ProcessInfo process);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool IsProcessInJob(IntPtr process, IntPtr job, out bool member);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern uint ResumeThread(IntPtr thread);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool GetExitCodeProcess(IntPtr process, out uint code);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool TerminateProcess(IntPtr process, uint code);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool TerminateJobObject(IntPtr job, uint code);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool CloseHandle(IntPtr handle);
    }
}
