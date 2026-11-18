export function Brand({ product }: { product: string }) {
  return <div className="brand">
    <svg className="brand__mark" viewBox="0 0 100 100" aria-hidden="true" focusable="false">
      <path fill="#065292" d="M0 32c0-7 4-12 11-15L49 1c9-4 17 1 17 10v7c0 3-1 5-6 6l-5 3c-3 1-5 3-7 3-9 0-16 8-16 18v39l-20 9C5 99 0 94 0 87V32Z" />
      <path fill="#45B035" d="M48 30c0-8 5-13 12-12 2 0 4 1 6 2l25 11c6 3 9 8 9 15v41c0 8-5 12-12 9l-24-9V48c0-10-7-18-16-18Z" />
    </svg>
    <span className="brand__name">O2 CONNECTIVE<small>{product}</small></span>
  </div>;
}
