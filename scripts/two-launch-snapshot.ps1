$ErrorActionPreference='Stop'; try {
    $rows=@(Get-CimInstance Win32_Process -Property Name,ExecutablePath -ErrorAction Stop | Select-Object Name,ExecutablePath)
    if ($rows.Count -gt 4096) { throw 'BOUND' }
    $ports=@(Get-NetTCPConnection -State Listen -ErrorAction Stop | Select-Object -ExpandProperty LocalPort)
    if ($ports.Count -gt 4096) { throw 'BOUND' }
    @{rows=$rows;ports=$ports}|ConvertTo-Json -Depth 4 -Compress
    } catch { exit 1 }
