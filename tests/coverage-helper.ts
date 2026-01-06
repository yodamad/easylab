import { Page, CoverageEntry } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

/**
 * Coverage collection helper for Playwright tests
 * Collects JavaScript coverage using Chrome DevTools Protocol
 */
export class CoverageCollector {
  private coverageEntries: CoverageEntry[] = [];
  private outputDir: string;

  constructor(outputDir: string = './coverage/frontend') {
    this.outputDir = outputDir;
    // Ensure output directory exists
    if (!fs.existsSync(this.outputDir)) {
      fs.mkdirSync(this.outputDir, { recursive: true });
    }
  }

  /**
   * Start collecting coverage for a page
   */
  async startCoverage(page: Page): Promise<void> {
    if (process.env.COLLECT_COVERAGE === 'true') {
      await page.coverage.startJSCoverage();
    }
  }

  /**
   * Stop collecting coverage and save results
   */
  async stopCoverage(page: Page, testName: string): Promise<void> {
    if (process.env.COLLECT_COVERAGE === 'true') {
      const coverage = await page.coverage.stopJSCoverage();
      this.coverageEntries.push(...coverage);
      
      // Save coverage for this test
      const safeTestName = testName.replace(/[^a-z0-9]/gi, '_').toLowerCase();
      const coverageFile = path.join(this.outputDir, `coverage-${safeTestName}.json`);
      fs.writeFileSync(coverageFile, JSON.stringify(coverage, null, 2));
    }
  }

  /**
   * Get all collected coverage entries
   */
  getCoverageEntries(): CoverageEntry[] {
    return this.coverageEntries;
  }

  /**
   * Calculate coverage statistics
   */
  calculateCoverageStats(): {
    totalBytes: number;
    usedBytes: number;
    coveragePercentage: number;
    files: number;
  } {
    if (this.coverageEntries.length === 0) {
      return {
        totalBytes: 0,
        usedBytes: 0,
        coveragePercentage: 0,
        files: 0,
      };
    }

    let totalBytes = 0;
    let usedBytes = 0;
    const uniqueFiles = new Set<string>();

    for (const entry of this.coverageEntries) {
      // Filter out non-application URLs (CDN, browser extensions, etc.)
      if (!entry.url.includes('localhost') && !entry.url.includes('127.0.0.1')) {
        continue;
      }
      
      uniqueFiles.add(entry.url);
      
      // Playwright coverage entries have ranges array
      if (entry.ranges) {
        for (const range of entry.ranges) {
          const rangeSize = range.endOffset - range.startOffset;
          totalBytes += rangeSize;
          // If count > 0, the code was executed
          if (range.count > 0) {
            usedBytes += rangeSize;
          }
        }
      }
    }

    const coveragePercentage = totalBytes > 0 ? (usedBytes / totalBytes) * 100 : 0;

    return {
      totalBytes,
      usedBytes,
      coveragePercentage,
      files: uniqueFiles.size,
    };
  }

  /**
   * Save summary report
   */
  saveSummaryReport(): void {
    const stats = this.calculateCoverageStats();
    const summary = {
      timestamp: new Date().toISOString(),
      ...stats,
      entries: this.coverageEntries.length,
    };

    const summaryFile = path.join(this.outputDir, 'coverage-summary.json');
    fs.writeFileSync(summaryFile, JSON.stringify(summary, null, 2));
    
    // Also create a human-readable summary
    const textSummary = `
Frontend Coverage Summary
========================
Timestamp: ${summary.timestamp}
Files Covered: ${stats.files}
Coverage Entries: ${summary.entries}
Total Bytes: ${stats.totalBytes}
Used Bytes: ${stats.usedBytes}
Coverage: ${stats.coveragePercentage.toFixed(2)}%
`;
    fs.writeFileSync(path.join(this.outputDir, 'coverage-summary.txt'), textSummary);
  }

  /**
   * Reset coverage collection
   */
  reset(): void {
    this.coverageEntries = [];
  }
}

// Global coverage collector instance
export const coverageCollector = new CoverageCollector();

