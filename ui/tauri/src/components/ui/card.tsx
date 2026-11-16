// Adapted from shadcn/ui; only the used Card root is retained.
import type { ComponentProps } from 'react';
export function Card({ className = '', ...props }: ComponentProps<'section'>) {
  return <section data-slot="card" className={`shell-panel ${className}`} {...props} />;
}
