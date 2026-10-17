/**
 * Seed categories and products in Gogo's product platform.
 *
 * PRODUCT_BASE_URL  default http://localhost:8082
 * CATEGORY_BASE_URL default http://localhost:8086
 * PRODUCT_PLATFORM_SERVICE_TOKEN overrides the local development credential
 * CATEGORY_COUNT    default 12
 * PRODUCT_COUNT     default 250
 * CONCURRENCY       default 12
 * DRY_RUN           set to 1 to print the plan without writing
 */

const productBase = (Bun.env.PRODUCT_BASE_URL ?? "http://localhost:8082").replace(/\/$/, "");
const categoryBase = (Bun.env.CATEGORY_BASE_URL ?? "http://localhost:8086").replace(/\/$/, "");
// This public, local-only credential matches docker-compose.dev.yml and
// docker-compose.yml. Bun's .env value takes precedence when the stack is
// configured with a custom token.
const serviceToken = (Bun.env.PRODUCT_PLATFORM_SERVICE_TOKEN ?? "local-dev-service-token-not-a-secret").trim();
const categoryCount = positiveInt("CATEGORY_COUNT", 12);
const productCount = positiveInt("PRODUCT_COUNT", 250);
const concurrency = positiveInt("CONCURRENCY", 12);
const dryRun = Bun.env.DRY_RUN === "1" || Bun.env.DRY_RUN === "true";

const categories = [
  "Elektronik", "Peralatan Rumah", "Fashion Pria", "Fashion Wanita", "Olahraga",
  "Kecantikan", "Buku", "Mainan", "Dapur", "Aksesori", "Komputer", "Outdoor",
  "Kesehatan", "Alat Tulis", "Otomotif", "Perawatan Bayi",
];
const brands = ["Nusantara", "Arunika", "Bumi Works", "Kirana", "Ruang", "Loka", "Sora", "Teras"];
const products = [
  "Botol Minum", "Lampu Meja", "Tas Harian", "Kemeja Katun", "Headphone Nirkabel",
  "Kopi Arabika", "Sepatu Lari", "Selimut Lembut", "Kamera Saku", "Sandal Santai",
  "Rak Serbaguna", "Mouse Ergonomis", "Dompet Kulit", "Skincare Harian", "Buku Catatan",
  "Panci Stainless", "Speaker Mini", "Jaket Hujan", "Bantal Leher", "Kabel USB-C",
];
const adjectives = ["Klasik", "Modern", "Ringkas", "Premium", "Alami", "Seri Baru", "Praktis", "Urban"];
const pick = <T>(values: T[]): T => values[Math.floor(Math.random() * values.length)]!;
const int = (min: number, max: number) => Math.floor(Math.random() * (max - min + 1)) + min;

function positiveInt(name: string, fallback: number): number {
  const value = Number(Bun.env[name] ?? fallback);
  if (!Number.isSafeInteger(value) || value < 1) throw new Error(`${name} must be a positive integer`);
  return value;
}

class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

async function post<T>(base: string, path: string, body: unknown): Promise<T> {
  if (dryRun) return { id: int(1, 2_000_000) } as T;
  let lastError: unknown;
  for (let attempt = 1; attempt <= 4; attempt++) {
    try {
      const response = await fetch(`${base}${path}`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(serviceToken ? { Authorization: `Bearer ${serviceToken}` } : {}),
        },
        body: JSON.stringify(body),
      });
      if (response.ok) return await response.json() as T;
      const detail = await response.text();
      const error = new ApiError(response.status, `POST ${path} returned ${response.status}: ${detail}`);
      if (response.status < 500 && response.status !== 429) throw error;
      lastError = error;
    } catch (error) {
      if (error instanceof ApiError && error.status !== 429) throw error;
      lastError = error;
    }
    await Bun.sleep(attempt * 250);
  }
  throw lastError;
}

async function pool(total: number, task: (index: number) => Promise<void>): Promise<number> {
  let next = 0;
  let failed = 0;
  await Promise.all(Array.from({ length: Math.min(concurrency, total) }, async () => {
    while (true) {
      const index = next++;
      if (index >= total) return;
      try {
        await task(index);
      } catch (error) {
        failed++;
        if (failed <= 10) console.error(`Failed item ${index + 1}:`, error);
      }
    }
  }));
  return failed;
}

interface Category { id: number }

async function main() {
  console.log(`Gogo product faker${dryRun ? " (dry run)" : ""}`);
  console.log(`Categories: ${categoryCount}; products: ${productCount}; concurrency: ${concurrency}`);

  const categoryIDs: number[] = [];
  let categoryFailures = 0;
  for (let i = 0; i < categoryCount; i++) {
    try {
      const category = await post<Category>(categoryBase, "/api/v1/categories", {
        name: `${pick(categories)} ${i + 1}`,
        description: `Kategori contoh ${i + 1} untuk katalog Gogo.`,
      });
      categoryIDs.push(category.id);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        throw new Error("Category Service rejected PRODUCT_PLATFORM_SERVICE_TOKEN. Use the same token configured as INBOUND_SERVICE_TOKENS in docker-compose.dev.yml.", { cause: error });
      }
      categoryFailures++;
      console.error(`Failed to create category ${i + 1}:`, error);
    }
  }
  if (categoryIDs.length === 0) throw new Error("No categories were created; cannot seed products.");

  const productFailures = await pool(productCount, async (index) => {
    const title = `${pick(adjectives)} ${pick(products)}`;
    const sku = `GOGO-${String(index + 1).padStart(7, "0")}`;
    await post(productBase, "/api/v1/products", {
      product_type_id: "general",
      schema_version: 1,
      // The general product type currently has no registered attributes.
      attributes: {},
      category_id: pick(categoryIDs),
      name: `${title} ${index + 1}`,
      description: `${title} pilihan dengan kualitas terjaga. Cocok untuk kebutuhan sehari-hari.`,
      brand: pick(brands),
      price_cents: int(250_000, 25_000_000),
      currency: "IDR",
      stock: int(0, 300),
      status: "active",
      variants: [{ sku, name: "Default", price_cents: int(250_000, 25_000_000), currency: "IDR", stock: int(0, 100) }],
    });
  });

  console.log(`Finished: ${categoryIDs.length}/${categoryCount} categories; ${productCount - productFailures}/${productCount} products.`);
  if (categoryFailures || productFailures) process.exitCode = 1;
}

main().catch((error) => {
  console.error("Product generation failed:", error);
  process.exitCode = 1;
});
