export async function callWithTimeout(operation: () => Promise<unknown> | undefined, budget: number) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try { return await Promise.race([operation(), new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error('timeout')), budget);
  })]); } finally { clearTimeout(timer); }
}
