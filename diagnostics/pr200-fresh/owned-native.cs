// Diagnostic read-only helpers. Raw identities/paths never leave this process.
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class OwnedCaptureNative {
  [DllImport("kernel32.dll", SetLastError=true)] static extern uint GetProcessId(IntPtr h);
  [DllImport("kernel32.dll", SetLastError=true)] static extern uint WaitForSingleObject(IntPtr h,uint ms);
  [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetProcessTimes(IntPtr h,out long created,out long exited,out long kernel,out long user);
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool QueryFullProcessImageName(IntPtr h,uint flags,StringBuilder text,ref uint size);
  [DllImport("shell32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr CommandLineToArgvW(string text,out int count);
  [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr p);
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr CreateFileW(string p,uint access,uint share,IntPtr security,uint disposition,uint flags,IntPtr template);
  [DllImport("kernel32.dll", SetLastError=true)] static extern bool CloseHandle(IntPtr h);
  [StructLayout(LayoutKind.Sequential, Pack=4)] struct Info {
    public uint Attributes; public long Creation,Access,Write;
    public uint Volume,SizeHigh,SizeLow,Links,IndexHigh,IndexLow;
  }
  [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetFileInformationByHandle(IntPtr h,out Info info);
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern uint GetFinalPathNameByHandleW(IntPtr h,StringBuilder name,uint length,uint flags);
  public static string State(IntPtr h,uint pid,long created,string image) {
    long c,e,k,u; uint size=32768; var name=new StringBuilder((int)size);
    uint actualPid=GetProcessId(h);
    if(actualPid==0 || !GetProcessTimes(h,out c,out e,out k,out u)) return "unavailable";
    if(actualPid!=pid || c!=created) return "identity-mismatch";
    uint wait=WaitForSingleObject(h,0);
    if(wait==0) return "exited";
    if(wait!=258 || !QueryFullProcessImageName(h,0,name,ref size)) return "unavailable";
    return String.Equals(name.ToString(),image,StringComparison.OrdinalIgnoreCase) ? "live" : "identity-mismatch";
  }

  public static string[] Argv(string text) {
    if(text==null || text.Length==0 || text.Length>32768 || text.IndexOf('\0')>=0) throw new InvalidOperationException();
    int count; IntPtr p=CommandLineToArgvW(text,out count);
    if(p==IntPtr.Zero) throw new InvalidOperationException();
    try {
      if(count<1 || count>256) throw new InvalidOperationException();
      var result=new string[count];
      for(int i=0;i<count;i++) result[i]=Marshal.PtrToStringUni(Marshal.ReadIntPtr(p,i*IntPtr.Size));
      return result;
    } finally { if(LocalFree(p)!=IntPtr.Zero) throw new InvalidOperationException(); }
  }
  static bool Same(Info a,Info b) { return a.Volume==b.Volume && a.IndexHigh==b.IndexHigh && a.IndexLow==b.IndexLow && a.Attributes==b.Attributes; }
  static string FinalName(IntPtr h) {
    var b=new StringBuilder(32768); uint n=GetFinalPathNameByHandleW(h,b,(uint)b.Capacity,0);
    if(n==0 || n>=b.Capacity) return null;
    string s=b.ToString();
    return s.StartsWith(@"\\?\",StringComparison.Ordinal) ? s.Substring(4).TrimEnd('\\') : null;
  }
  static string ProbeDirectories(string root,string actual,List<IntPtr> handles) {
    var paths=new List<string>(); var infos=new List<Info>();
    var unique=new HashSet<string>(StringComparer.OrdinalIgnoreCase);
    foreach(string leaf in new[]{root,actual}) {
      string current=leaf;
      for(int depth=0;;depth++) {
        if(depth>=32) return "directory-limit";
        if(unique.Add(current)) paths.Add(current);
        if(current.Length<=3) break;
        int split=current.LastIndexOf('\\');
        if(split<2) return "directory-unavailable";
        current=split==2 ? current.Substring(0,3) : current.Substring(0,split);
      }
    }
    foreach(string path in paths) {
      IntPtr h=CreateFileW(path,0x80,7,IntPtr.Zero,3,0x02200000,IntPtr.Zero);
      if(h==new IntPtr(-1)) return "directory-unavailable";
      handles.Add(h); Info info;
      if(!GetFileInformationByHandle(h,out info)) return "directory-unavailable";
      if((info.Attributes & 0x400)!=0) return "directory-reparse";
      if((info.Attributes & 0x10)==0) return "directory-unavailable";
      if(!String.Equals(FinalName(h),path.TrimEnd('\\'),StringComparison.OrdinalIgnoreCase)) return "directory-alias";
      infos.Add(info);
    }
    for(int i=0;i<handles.Count;i++) {
      Info after;
      if(!GetFileInformationByHandle(handles[i],out after) || !Same(infos[i],after) || !String.Equals(FinalName(handles[i]),paths[i].TrimEnd('\\'),StringComparison.OrdinalIgnoreCase)) return "directory-changed";
    }
    return "ok";
  }
  public static bool CloseHandles(List<IntPtr> handles,Func<IntPtr,bool> close) {
    bool ok=true;
    foreach(IntPtr h in handles) { try { if(!close(h)) ok=false; } catch { ok=false; } }
    return ok;
  }
  public static string Directories(string root,string actual) {
    var handles=new List<IntPtr>(); string code="directory-unavailable";
    try { code=ProbeDirectories(root,actual,handles); }
    catch { code="directory-unavailable"; }
    finally { if(!CloseHandles(handles,CloseHandle)) code="directory-dispose-uncertain"; }
    return code;
  }
}
