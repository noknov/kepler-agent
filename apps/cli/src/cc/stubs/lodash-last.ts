export default function last<T>(arr: T[]): T | undefined {
  return arr[arr.length - 1];
}
