using System;
using System.Globalization;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using Tether.OwnerProbe;

namespace Tether.AppOwnerFixture {
    internal static class FixtureNative {
        [DllImport("kernel32.dll", SetLastError=true, CharSet=CharSet.Unicode)]
        internal static extern IntPtr CreateEventW(IntPtr attributes, bool manual, bool initial, string name);
        [DllImport("kernel32.dll", SetLastError=true)]
        internal static extern bool SetEvent(IntPtr handle);

        internal static void Require(bool condition) {
            if (!condition) throw new InvalidOperationException("APP_OWNER_FIXTURE_FAILED");
        }
        internal static void Member(IntPtr process, IntPtr job) {
            bool member;
            Require(Native.IsProcessInJob(process, job, out member) && member);
        }
        // Fixed self-executable only. No arbitrary image, argument, PID, or caller receipt API.
        internal static Native.ProcessInfo Create(string role, IntPtr[] inherited) {
            Require(role == "root" || role == "child");
            IntPtr size = IntPtr.Zero;
            Require(!DescendantNative.InitializeProcThreadAttributeList(IntPtr.Zero, 1, 0, ref size)
                && Marshal.GetLastWin32Error() == 122 && size.ToInt64() > 0);
            IntPtr list = Marshal.AllocHGlobal(size);
            IntPtr handles = IntPtr.Zero, environment = IntPtr.Zero;
            bool initialized = false;
            try {
                handles = Marshal.AllocHGlobal(inherited.Length * IntPtr.Size);
                environment = Marshal.AllocHGlobal(4);
                Marshal.WriteInt32(environment, 0);
                initialized = DescendantNative.InitializeProcThreadAttributeList(list, 1, 0, ref size);
                Require(initialized);
                for (int i = 0; i < inherited.Length; i++)
                    Marshal.WriteIntPtr(handles, i * IntPtr.Size, inherited[i]);
                Require(DescendantNative.UpdateProcThreadAttribute(list, 0, new IntPtr(0x20002),
                    handles, new IntPtr(inherited.Length * IntPtr.Size), IntPtr.Zero, IntPtr.Zero));
                var startup = new DescendantNative.StartupEx();
                startup.startup.cb = (uint)Marshal.SizeOf(typeof(DescendantNative.StartupEx));
                startup.attributes = list;
                string image = Assembly.GetExecutingAssembly().Location;
                var command = new StringBuilder("\"" + image + "\" " + role);
                foreach (IntPtr handle in inherited)
                    command.Append(" " + handle.ToInt64().ToString(CultureInfo.InvariantCulture));
                Native.ProcessInfo process;
                Require(DescendantNative.CreateProcessW(image, command, IntPtr.Zero, IntPtr.Zero,
                    true, 0x08080404, environment, Environment.SystemDirectory, ref startup, out process));
                return process;
            } finally {
                if (initialized) DescendantNative.DeleteProcThreadAttributeList(list);
                if (environment != IntPtr.Zero) Marshal.FreeHGlobal(environment);
                if (handles != IntPtr.Zero) Marshal.FreeHGlobal(handles);
                Marshal.FreeHGlobal(list);
            }
        }
    }
}
